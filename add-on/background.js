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
async function processMessagePage(folder, account) {
    const unreadItems = [];
    try {
        let page = await browser.messages.list(folder.id);
        while (page) {
            // Optimize: Fetch full messages in parallel batches
            const unreadMessages = page.messages.filter((m) => !m.read);

            const batchPromises = unreadMessages.map(async (message) => {
                try {
                    const fullMessage = await browser.messages.getFull(message.id);
                    return buildUnreadItem(message, fullMessage, account);
                } catch (e) {
                    console.error(`Error fetching full message ${message.id}`, e);
                    return null;
                }
            });

            const items = await Promise.all(batchPromises);
            unreadItems.push(...items.filter((i) => i !== null));

            page = page.id ? await browser.messages.continueList(page.id) : null;
        }
    } catch (e) {
        console.error(`Error listing messages for folder ${folder.name}:`, e);
    }
    return unreadItems;
}

/*
Recursively process folders
*/
async function processFolders(folders, account, unreadItems) {
    for (const folderRef of folders) {
        let folder;
        try {
            folder = await browser.folders.get(folderRef.id);
        } catch (e) {
            console.error(`Failed to get folder ${folderRef.name}:`, e);
            continue;
        }

        // Skip Trash folder
        if (folder.type === "trash") {
            continue;
        }

        // Process folder messages
        const items = await processMessagePage(folder, account);
        unreadItems.push(...items);

        // Recurse into subfolders
        if (folder.subFolders && folder.subFolders.length > 0) {
            await processFolders(folder.subFolders, account, unreadItems);
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
