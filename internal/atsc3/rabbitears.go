// Package atsc3 reads the RabbitEars.info list of ATSC 3.0 (NextGen TV)
// transmitters and the services each one carries.
package atsc3

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"golang.org/x/net/html"

	"airwaves/internal/web"
)

const listURL = "https://www.rabbitears.info/market.php?request=atsc3"

// Host is a transmitter broadcasting ATSC 3.0.
type Host struct {
	Market     string    `json:"market"`
	CallSign   string    `json:"callSign"`
	FacilityID int       `json:"facilityId"`
	RF         string    `json:"rf"` // may describe a move, e.g. "2 → 3"
	Launched   string    `json:"launched"`
	Simulcast  string    `json:"simulcast"` // station hosting this one's ATSC 1.0 signal, or "N/A"
	Services   []Service `json:"services"`
}

// Service is one virtual channel inside an ATSC 3.0 multiplex.
type Service struct {
	Display string `json:"display"` // "02-1"
	Major   int    `json:"major"`
	Minor   int    `json:"minor"`
	Network string `json:"network"`
	Name    string `json:"name"` // usually the originating call sign
	// ATSC1Call is the transmitter carrying this service's ATSC 1.0
	// simulcast, when there is one.
	ATSC1Call    string `json:"atsc1Call"`
	ATSC1Display string `json:"atsc1Display"`
}

// Fetch downloads and parses the national ATSC 3.0 list.
func Fetch(ctx context.Context, c *http.Client) ([]Host, error) {
	body, err := web.Get(ctx, c, listURL)
	if err != nil {
		return nil, fmt.Errorf("rabbitears atsc3 list: %w", err)
	}
	return Parse(body)
}

type cell struct {
	text string
	href string
}

var (
	marketRank = regexp.MustCompile(`^\d+$`)
	display    = regexp.MustCompile(`^(\d+)-(\d+)$`)
	source     = regexp.MustCompile(`^\(\s*(\S+)\s+(\d+-\d+)\s*\)$`)
)

// Parse decodes the RabbitEars ATSC 3.0 list page. The page is one table:
// market header rows (rank, name), host rows (call, RF, launch date, 1.0
// simulcast host) and service rows (bullet, display channel, network, name,
// 1.0 source).
func Parse(page []byte) ([]Host, error) {
	doc, err := html.Parse(bytes.NewReader(page))
	if err != nil {
		return nil, fmt.Errorf("parse atsc3 list: %w", err)
	}
	var hosts []Host
	var market string
	for _, row := range rows(doc) {
		switch {
		case len(row) == 2 && marketRank.MatchString(row[0].text):
			market = row[1].text
		case len(row) == 5 && row[0].text == "" && market != "" && row[1].text != "":
			hosts = append(hosts, Host{
				Market:     market,
				CallSign:   row[1].text,
				FacilityID: facilityID(row[1].href),
				RF:         row[2].text,
				Launched:   row[3].text,
				Simulcast:  row[4].text,
			})
		case len(row) == 6 && row[1].text == "•" && len(hosts) > 0:
			m := display.FindStringSubmatch(row[2].text)
			if m == nil {
				continue
			}
			s := Service{Display: row[2].text, Network: row[3].text, Name: row[4].text}
			s.Major, _ = strconv.Atoi(m[1])
			s.Minor, _ = strconv.Atoi(m[2])
			if src := source.FindStringSubmatch(row[5].text); src != nil {
				s.ATSC1Call, s.ATSC1Display = src[1], src[2]
			}
			h := &hosts[len(hosts)-1]
			h.Services = append(h.Services, s)
		}
	}
	if len(hosts) == 0 {
		return nil, fmt.Errorf("parse atsc3 list: no stations found, page layout may have changed")
	}
	return hosts, nil
}

// facilityID extracts the facility ID from links like
// "market.php?request=station_search&callsign=56043#station".
func facilityID(href string) int {
	u, err := url.Parse(href)
	if err != nil {
		return 0
	}
	id, _ := strconv.Atoi(u.Query().Get("callsign"))
	return id
}

func rows(n *html.Node) [][]cell {
	var out [][]cell
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode && n.Data == "tr" {
			var row []cell
			for c := n.FirstChild; c != nil; c = c.NextSibling {
				if c.Type == html.ElementNode && (c.Data == "td" || c.Data == "th") {
					row = append(row, cell{text: text(c), href: firstHref(c)})
				}
			}
			out = append(out, row)
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(n)
	return out
}

func text(n *html.Node) string {
	var b strings.Builder
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.TextNode {
			b.WriteString(n.Data)
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(n)
	return strings.Join(strings.Fields(strings.ReplaceAll(b.String(), " ", " ")), " ")
}

func firstHref(n *html.Node) string {
	if n.Type == html.ElementNode && n.Data == "a" {
		for _, a := range n.Attr {
			if a.Key == "href" {
				return a.Val
			}
		}
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if h := firstHref(c); h != "" {
			return h
		}
	}
	return ""
}
