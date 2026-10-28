package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"downloadcenter/internal/trackers"
)

// newTrackon lists the trackers it has seen working for a while, one per
// line (https://newtrackon.com/api).
const newTrackon = "https://newtrackon.com/api/stable"

// trackerList writes trackers.json for the site: newTrackon's stable list
// checked with the rules dcd applies; when that fails the published file
// (-prev) is kept, and when that fails too nothing is written, so the site
// still deploys and dcd keeps the list it has.
func trackerList(args []string) error {
	o := parse("trackers", args)
	if err := need(o.out); err != nil {
		return err
	}
	b, from, err := buildTrackers(&http.Client{Timeout: 30 * time.Second}, *o.source, *o.prev, time.Now(), trackers.DefaultLookup)
	if err != nil {
		fmt.Fprintln(os.Stderr, "trackers.json not written: "+err.Error())
		return nil
	}
	if err := os.WriteFile(*o.out, b, 0644); err != nil {
		return err
	}
	fmt.Printf("%s: from %s\n", *o.out, from)
	return nil
}

// buildTrackers makes trackers.json from the source, else from prev. Names
// that resolve inside a LAN (or to loopback) are left out either way.
func buildTrackers(c *http.Client, source, prev string, now time.Time, lookup trackers.Lookup) ([]byte, string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	write := func(list []string, from string) ([]byte, string, error) {
		if list = trackers.DropInternal(ctx, list, lookup); len(list) == 0 {
			return nil, "", errors.New("no usable tracker")
		}
		b, _ := json.MarshalIndent(trackers.Feed{Schema: 1, Generated: now.Unix(), Source: "newtrackon", Trackers: list}, "", "  ")
		return append(b, '\n'), from, nil
	}
	text, err := fetch(c, source)
	if err == nil {
		var b []byte
		var from string
		if b, from, err = write(trackers.Clean(strings.Split(string(text), "\n")), source); err == nil {
			return b, from, nil
		}
	}
	fmt.Fprintf(os.Stderr, "%s: %v\n", source, err)
	if prev == "" {
		return nil, "", err
	}
	b, perr := fetch(c, prev)
	var list []string
	if perr == nil {
		list, perr = trackers.ParseFeed(b)
	}
	if perr == nil {
		var out []byte
		if out, _, perr = write(list, prev); perr == nil {
			return out, prev, nil
		}
	}
	return nil, "", fmt.Errorf("%v; %s: %v", err, prev, perr)
}

func fetch(c *http.Client, u string) ([]byte, error) {
	resp, err := c.Get(u)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, trackers.MaxFeedSize+1))
	if err == nil && len(b) > trackers.MaxFeedSize {
		err = errors.New("too large")
	}
	return b, err
}
