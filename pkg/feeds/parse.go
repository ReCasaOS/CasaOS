package feeds

import (
	"encoding/xml"
	"errors"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"unicode/utf8"

	"golang.org/x/net/html/charset"
)

const (
	maxBody        = 2 << 20
	maxTitleRunes  = 300
	maxSourceRunes = 80
)

var tags = regexp.MustCompile(`<[^>]*>`)

type atomLink struct {
	Href string `xml:"href,attr"`
	Rel  string `xml:"rel,attr"`
}

// rssItem keeps every <link> of an item: a feed that also names itself with an
// <atom:link href=".." rel="self"/> after the page's <link> must not lose the page.
type rssItem struct {
	Title string `xml:"title"`
	Links []struct {
		Text string `xml:",chardata"`
	} `xml:"link"`
}

// link is the first <link> with text.
func (i rssItem) link() string {
	for _, l := range i.Links {
		if strings.TrimSpace(l.Text) != "" {
			return l.Text
		}
	}
	return ""
}

// document is RSS 2.0 (<rss><channel><item>), RSS 1.0 (<rdf:RDF><channel/><item>)
// and Atom (<feed><entry>) at once: each fills only its own fields.
type document struct {
	ChannelTitle string    `xml:"channel>title"`
	RSSItems     []rssItem `xml:"channel>item"`
	RDFItems     []rssItem `xml:"item"`
	FeedTitle    string    `xml:"title"`
	Entries      []struct {
		Title string     `xml:"title"`
		Links []atomLink `xml:"link"`
	} `xml:"entry"`
}

// fetch reads one feed and answers its first maxItemsPerFeed headlines that have a
// title and an http(s) link.
func fetch(client *http.Client, feed string) ([]Item, error) {
	req, err := http.NewRequest(http.MethodGet, feed, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "feed-reader/1.0")
	req.Header.Set("Accept", "application/rss+xml, application/atom+xml, application/xml, text/xml;q=0.9, */*;q=0.1")
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("answered %d", resp.StatusCode)
	}
	return parse(io.LimitReader(resp.Body, maxBody), resp.Request.URL)
}

func parse(r io.Reader, base *url.URL) ([]Item, error) {
	decoder := xml.NewDecoder(r)
	decoder.CharsetReader = charset.NewReaderLabel
	decoder.Strict = false
	decoder.Entity = xml.HTMLEntity
	var doc document
	if err := decoder.Decode(&doc); err != nil {
		return nil, err
	}

	source := text(doc.ChannelTitle, maxSourceRunes)
	if source == "" {
		source = text(doc.FeedTitle, maxSourceRunes)
	}
	if source == "" {
		source = base.Hostname()
	}

	type raw struct{ title, link string }
	var all []raw
	for _, i := range append(doc.RSSItems, doc.RDFItems...) {
		all = append(all, raw{i.Title, i.link()})
	}
	for _, e := range doc.Entries {
		all = append(all, raw{e.Title, entryLink(e.Links)})
	}
	if len(all) == 0 && doc.ChannelTitle == "" && doc.FeedTitle == "" {
		return nil, errors.New("not an RSS or Atom feed")
	}

	items := []Item{}
	for _, r := range all {
		title, link := text(r.title, maxTitleRunes), absolute(base, r.link)
		if title == "" || link == "" {
			continue
		}
		items = append(items, Item{Title: title, Link: link, Source: source})
		if len(items) == maxItemsPerFeed {
			break
		}
	}
	return items, nil
}

// entryLink is an Atom entry's page: its alternate link, else its first.
func entryLink(links []atomLink) string {
	for _, l := range links {
		if (l.Rel == "" || l.Rel == "alternate") && strings.TrimSpace(l.Href) != "" {
			return l.Href
		}
	}
	if len(links) > 0 {
		return links[0].Href
	}
	return ""
}

// absolute is link resolved against the feed's address, "" unless http(s): the
// dashboard opens it.
func absolute(base *url.URL, link string) string {
	link = strings.TrimSpace(link)
	if link == "" {
		// resolved, it would be the feed's own address
		return ""
	}
	u, err := url.Parse(link)
	if err != nil {
		return ""
	}
	u = base.ResolveReference(u)
	if (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return ""
	}
	return u.String()
}

// text is s as one line of plain text: markup dropped, entities decoded, spaces
// collapsed, cut to max runes.
func text(s string, max int) string {
	s = html.UnescapeString(tags.ReplaceAllString(html.UnescapeString(s), " "))
	s = strings.Join(strings.Fields(s), " ")
	if utf8.RuneCountInString(s) > max {
		s = string([]rune(s)[:max-1]) + "…"
	}
	return s
}
