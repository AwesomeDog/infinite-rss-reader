package main

import (
	"bytes"
	"strings"
	"testing"
)

// normalizeInput is a deliberately messy Thunderbird-style export: a duplicate
// xmlUrl in both a wrapper and a folder, two outlines sharing a title, an empty
// folder, an outline without a title, markup characters in text and attributes,
// plus an XML declaration, a comment and a processing instruction that a
// normalizing pass is expected to drop.
const normalizeInput = `<?xml version="1.0" encoding="UTF-8"?>
<!-- a comment that Python's parser drops -->
<?pi target?>
<opml version="1.0" xmlns:fz="urn:forumzilla:">
  <head>
    <title>Toms &amp; Jerry &lt;feeds&gt; "quoted"</title>
    <dateCreated>Sun, 04 Oct 2026 07:40:26 GMT</dateCreated>
  </head>
  <body>
    <outline title="zeta">
      <outline title="Dup &amp; Feed">
        <outline type="rss" title="Dup &amp; Feed" text="Dup &amp; Feed" version="RSS" fz:quickMode="false" fz:options="{&quot;version&quot;:2,&quot;updates&quot;:{&quot;enabled&quot;:true,&quot;updateMinutes&quot;:100,&quot;lastUpdateTime&quot;:1791099313609}}" xmlUrl="https://example.com/dup.xml" htmlUrl="https://example.com/"/>
      </outline>
      <outline title="Alpha">
        <outline type="rss" title="Alpha" text="Alpha" version="RSS" fz:quickMode="true" fz:options="{&quot;version&quot;:2}" xmlUrl="https://example.com/a.xml" htmlUrl="https://example.com/"/>
      </outline>
    </outline>
    <outline title="beta">
      <outline type="rss" title="Dup &amp; Feed" text="Dup &amp; Feed" version="RSS" fz:quickMode="false" fz:options="{&quot;version&quot;:2}" xmlUrl="https://example.com/dup.xml" htmlUrl="https://example.com/"/>
      <outline type="rss" text="t" xmlUrl="https://example.com/b.xml" htmlUrl="https://example.com/"/>
    </outline>
    <outline title="mid">
      <outline title="Same Title">
        <outline type="rss" title="Same Title" text="Same Title" version="RSS" fz:quickMode="false" fz:options="{&quot;v&quot;:1}" xmlUrl="https://example.com/s2.xml" htmlUrl="https://example.com/"/>
      </outline>
      <outline title="Same Title">
        <outline type="rss" title="Same Title" text="Same Title" version="RSS" fz:quickMode="false" fz:options="{&quot;v&quot;:1}" xmlUrl="https://example.com/s1.xml" htmlUrl="https://example.com/"/>
      </outline>
    </outline>
    <outline title="empty folder"/>
  </body>
</opml>
`

// normalizeGolden is the output of the original Python script on normalizeInput,
// byte for byte. The Go port must keep matching it.
const normalizeGolden = `<opml xmlns:fz="urn:forumzilla:" version="1.0">
  <head>
    <title>Toms &amp; Jerry &lt;feeds&gt; "quoted"</title>
    <dateCreated>Sun, 04 Oct 2026 07:40:26 GMT</dateCreated>
  </head>
  <body>
    <outline title="beta">
      <outline type="rss" text="t" xmlUrl="https://example.com/b.xml" htmlUrl="https://example.com/" />
    </outline>
    <outline title="empty folder" />
    <outline title="mid">
      <outline title="Same Title">
        <outline type="rss" title="Same Title" text="Same Title" version="RSS" fz:quickMode="false" xmlUrl="https://example.com/s2.xml" htmlUrl="https://example.com/" />
      </outline>
      <outline title="Same Title">
        <outline type="rss" title="Same Title" text="Same Title" version="RSS" fz:quickMode="false" xmlUrl="https://example.com/s1.xml" htmlUrl="https://example.com/" />
      </outline>
    </outline>
    <outline title="zeta">
      <outline title="Alpha">
        <outline type="rss" title="Alpha" text="Alpha" version="RSS" fz:quickMode="true" xmlUrl="https://example.com/a.xml" htmlUrl="https://example.com/" />
      </outline>
      <outline title="Dup &amp; Feed">
        <outline type="rss" title="Dup &amp; Feed" text="Dup &amp; Feed" version="RSS" fz:quickMode="false" xmlUrl="https://example.com/dup.xml" htmlUrl="https://example.com/" />
      </outline>
    </outline>
  </body>
</opml>
`

func TestNormalizeOPMLMatchesGolden(t *testing.T) {
	out, err := normalizeOPML(strings.NewReader(normalizeInput))
	if err != nil {
		t.Fatalf("normalizeOPML: %v", err)
	}
	if got := string(out); got != normalizeGolden {
		t.Errorf("output mismatch\n got: %q\nwant: %q", got, normalizeGolden)
	}
}

func TestNormalizeOPMLIsIdempotent(t *testing.T) {
	first, err := normalizeOPML(strings.NewReader(normalizeInput))
	if err != nil {
		t.Fatalf("normalizeOPML: %v", err)
	}
	second, err := normalizeOPML(bytes.NewReader(first))
	if err != nil {
		t.Fatalf("normalizeOPML: %v", err)
	}
	if !bytes.Equal(first, second) {
		t.Errorf("not idempotent\nfirst:  %q\nsecond: %q", first, second)
	}
}

// fz:options embeds a timestamp and must go; fz:quickMode is a stable user
// preference and must stay.
func TestNormalizeOPMLStripsOptionsKeepsQuickMode(t *testing.T) {
	out, err := normalizeOPML(strings.NewReader(normalizeInput))
	if err != nil {
		t.Fatalf("normalizeOPML: %v", err)
	}
	got := string(out)
	if strings.Contains(got, "fz:options") {
		t.Error("fz:options was not stripped")
	}
	for _, want := range []string{`fz:quickMode="false"`, `fz:quickMode="true"`} {
		if !strings.Contains(got, want) {
			t.Errorf("%s was dropped", want)
		}
	}
}

func TestNormalizeOPMLRejectsMalformedXML(t *testing.T) {
	if _, err := normalizeOPML(strings.NewReader("<opml><body></opml>")); err == nil {
		t.Error("expected an error for malformed XML")
	}
}
