package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"time"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/launcher"
	"github.com/go-rod/rod/lib/proto"
)

func main() {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	wsURL, err := launcher.New().Headless(true).Launch()
	if err != nil {
		log.Fatal(err)
	}
	b := rod.New().ControlURL(wsURL)
	if err := b.Connect(); err != nil {
		log.Fatal(err)
	}
	defer func() { _ = b.Close() }()

	// 1. Typed request into a typed result: proto structs end-to-end.
	version, err := (proto.BrowserGetVersion{}).Call(b)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println("product:", version.Product)

	page, err := b.Page(proto.TargetCreateTarget{URL: "about:blank"})
	if err != nil {
		log.Fatal(err)
	}
	if err := page.Navigate("https://example.com"); err != nil {
		log.Fatal(err)
	}
	_ = page.WaitStable(300 * time.Millisecond)

	// 2. Typed page-level command through the page's own CDP session.
	metrics, err := (proto.PageGetLayoutMetrics{}).Call(page)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println("page height:", metrics.CSSContentSize.Height)

	// 3. Same wire call without proto: empty sessionID rides the page
	// session by convention, params as a plain map, decode into the
	// generated result type yourself.
	data, err := page.Call(ctx, "", "DOM.getDocument",
		map[string]interface{}{"depth": 2})
	if err != nil {
		log.Fatal(err)
	}
	var doc proto.DOMGetDocumentResult
	if err := json.Unmarshal(data, &doc); err != nil {
		log.Fatal(err)
	}
	fmt.Println("root:", doc.Root.NodeName)

	// 4. Events: one-shot waits and typed subscription loops.
	if err := (proto.PageSetLifecycleEventsEnabled{Enabled: true}).Call(page); err != nil {
		log.Fatal(err)
	}
	var navCount int
	go page.EachEvent(func(e *proto.PageLifecycleEvent) bool {
		if e.Name == proto.PageLifecycleEventNameNetworkIdle {
			navCount++
			return true
		}
		return false
	})()

	wait := page.WaitEvent(&proto.PageLoadEventFired{})
	if err := page.Navigate("https://example.com"); err != nil {
		log.Fatal(err)
	}
	wait()

	var loadFired proto.PageLoadEventFired
	for msg := range page.Event() {
		if msg.Load(&loadFired) {
			break
		}
	}
	fmt.Println(navCount, loadFired.ProtoEvent())
}
