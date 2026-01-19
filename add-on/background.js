/*
On startup, connect to the "rss_bridge" app.
*/
console.log("RSS Native Messaging Extension started");
let port = browser.runtime.connectNative("rss_bridge");

// Reconnection logic
function connect() {
    port = browser.runtime.connectNative("rss_bridge");
    port.onMessage.addListener(handleMessage);
    port.onDisconnect.addListener(handleDisconnect);
}

function handleDisconnect(p) {
    if (p.error) {
        console.log(`Disconnected due to an error: ${p.error.message}`);
    } else {
        console.log(`Disconnected`, p);
    }
    // Attempt to reconnect after a delay
    setTimeout(connect, 5000);
}

/*
Extract text and HTML content from message parts
*/
function extractMessageContent(parts) {
    let bodyContent = "";
    let bodyHtml = "";

    const stack = [...parts];
    while (stack.length > 0) {
        const part = stack.pop();
        if (part.contentType === "text/plain" && part.body && !bodyContent) {
            bodyContent = part.body;
        } else if (part.contentType === "text/html" && part.body && !bodyHtml) {
            bodyHtml = part.body;
        }

        if (part.parts && part.parts.length > 0) {
            stack.push(...part.parts);
        }
    }

    return {bodyContent, bodyHtml};
}

/*
Build unread message item object
*/
function buildUnreadItem(message, fullMessage, account) {
    const {bodyContent, bodyHtml} = fullMessage.parts
        ? extractMessageContent(fullMessage.parts)
        : {bodyContent: "", bodyHtml: ""};

    return {
        // Basic information
        id: message.id,
        subject: message.subject || "(No Subject)",
        author: message.author || "",
        // Time information
        date: message.date,
        // Folder and account information
        folder: message.folder.name,
        folderPath: message.folder.path || message.folder.name,
        accountName: account.name,
        accountType: account.type,
        // Full content
        body: bodyContent || bodyHtml || "",
        bodyHtml: bodyHtml,
        bodyText: bodyContent,
        // Metadata
        flagged: message.flagged || false,
        headers: fullMessage.headers || {},
        size: message.size || 0,
        // RSS-specific fields (extracted from headers)
        link: fullMessage.parts[0].headers?.["content-base"]?.[0] || "",
        guid: fullMessage.headers?.["message-id"]?.[0] || "",
    };
}

/*
Process message list pagination
*/
async function processMessagePage(folder, account, unreadOnly = true) {
    const items = [];
    try {
        let page = await browser.messages.list(folder.id);
        while (page) {
            const messages = unreadOnly ? page.messages.filter((m) => !m.read) : page.messages;

            const batchPromises = messages.map(async (message) => {
                try {
                    const fullMessage = await browser.messages.getFull(message.id);
                    return buildUnreadItem(message, fullMessage, account);
                } catch (e) {
                    console.error(`Error fetching full message ${message.id}`, e);
                    return null;
                }
            });

            const batchItems = await Promise.all(batchPromises);
            items.push(...batchItems.filter((i) => i !== null));

            page = page.id ? await browser.messages.continueList(page.id) : null;
        }
    } catch (e) {
        console.error(`Error listing messages for folder ${folder.name}:`, e);
    }
    return items;
}

/*
Recursively process folders
*/
async function processFolders(folders, account, items, options = {}) {
    const {unreadOnly = true, targetFolderPath = null} = options;

    for (const folderRef of folders) {
        let folder;
        try {
            folder = await browser.folders.get(folderRef.id);
        } catch (e) {
            console.error(`Failed to get folder ${folderRef.name}:`, e);
            continue;
        }

        if (folder.type === "trash") continue;

        // If targetFolderPath specified, only process matching folders
        const folderPath = folder.path || folder.name;
        const shouldProcess = !targetFolderPath || folderPath.startsWith(targetFolderPath);

        if (shouldProcess) {
            const folderItems = await processMessagePage(folder, account, unreadOnly);
            items.push(...folderItems);
        }

        // Recurse into subfolders
        if (folder.subFolders && folder.subFolders.length > 0) {
            await processFolders(folder.subFolders, account, items, options);
        }
    }
}

async function getUnreadRSSItems() {
    try {
        console.log("Fetching unread RSS items...");
        const accounts = await browser.accounts.list();
        const rssAccounts = accounts.filter((a) => a.type === "rss");
        const unreadItems = [];

        for (const account of rssAccounts) {
            if (account.folders) {
                await processFolders(account.folders, account, unreadItems);
            }
        }

        console.log(`Found ${unreadItems.length} unread items total`);
        return unreadItems;
    } catch (error) {
        console.error("Error fetching RSS items:", error);
        return [];
    }
}

/*
Get a single RSS item by ID (regardless of read status)
*/
async function getSingleItem(itemId) {
    try {
        const numericId = parseInt(itemId, 10);
        if (isNaN(numericId)) return null;

        const message = await browser.messages.get(numericId);
        if (!message) return null;

        const fullMessage = await browser.messages.getFull(numericId);
        const accounts = await browser.accounts.list();
        const account = accounts.find((a) => a.id === message.folder.accountId) || {name: "", type: ""};

        return buildUnreadItem(message, fullMessage, account);
    } catch (error) {
        console.error(`Error getting single item ${itemId}:`, error);
        return null;
    }
}

/*
Get all RSS items in a folder and its subfolders (including read items)
*/
async function getFolderItems(targetFolderPath) {
    try {
        console.log(`Fetching items for folder: ${targetFolderPath}`);
        const accounts = await browser.accounts.list();
        const rssAccounts = accounts.filter((a) => a.type === "rss");
        const allItems = [];

        for (const account of rssAccounts) {
            if (account.folders) {
                await processFolders(account.folders, account, allItems, {
                    unreadOnly: false,
                    targetFolderPath
                });
            }
        }

        // Sort by date descending (newest first)
        allItems.sort((a, b) => new Date(b.date) - new Date(a.date));
        console.log(`Found ${allItems.length} items in folder ${targetFolderPath}`);
        return allItems;
    } catch (error) {
        console.error(`Error fetching folder items for ${targetFolderPath}:`, error);
        return [];
    }
}

async function markItemAsRead(itemId) {
    try {
        const numericId = parseInt(itemId, 10);
        if (isNaN(numericId)) return false;
        await browser.messages.update(numericId, {read: true});
        return true;
    } catch (error) {
        console.error(`Error marking item ${itemId} as read:`, error);
        return false;
    }
}

async function handleMessage(message) {
    console.log("Received from native app:", message);
    if (typeof message === "object") {
        if (message.action === "getUnreadRSS") {
            let items = await getUnreadRSSItems();
            port.postMessage({type: "rssData", data: items});
        } else if (message.action === "getSingleItem") {
            let item = await getSingleItem(message.itemId);
            port.postMessage({
                type: "singleItemData",
                data: item,
                itemId: message.itemId,
            });
        } else if (message.action === "getFolderItems") {
            let items = await getFolderItems(message.folderPath);
            port.postMessage({
                type: "folderData",
                data: items,
                folderPath: message.folderPath,
            });
        } else if (message.action === "markAsRead") {
            let success = await markItemAsRead(message.itemId);
            port.postMessage({
                type: "markReadResult",
                itemId: message.itemId,
                success: success,
            });
        }
    }
}

/*
Update RSS data regularly (every 1 min)
*/
async function updateRSSData() {
    let items = await getUnreadRSSItems();
    port.postMessage({type: "rssData", data: items});
}

// Initial connection setup
port.onMessage.addListener(handleMessage);
port.onDisconnect.addListener(handleDisconnect);

// Update immediately on startup
updateRSSData();

// Update regularly
setInterval(updateRSSData, 1000 * 60 * 1);

/*
When the extension's action icon is clicked, manually trigger an update.
*/
browser.browserAction.onClicked.addListener(() => {
    console.log("Manual RSS update triggered");
    updateRSSData();
});
