package main

import (
	"context"
	"log"
	"strconv"
	"strings"
	"sync"

	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/services/dock"
	"github.com/wailsapp/wails/v3/pkg/services/notifications"
)

// attention is the shell's half of "a turn finished or asked a question while
// you were away": a native notification (at:notify) and the dock badge
// (at:badge). The UI decides when; the shell only knows how. Both services
// start lazily on first use, and a platform or build that cannot host them
// (an unbundled macOS binary has no bundle id for notifications) turns that
// half into a no-op instead of failing the app.
type attention struct {
	app *application.App

	notifyOnce sync.Once
	notifier   *notifications.NotificationService

	badgeOnce sync.Once
	badger    *dock.DockService
	mu        sync.Mutex
	want      int // the newest count the UI asked for
	badge     int // the count on the dock now
}

func installAttention(app *application.App) {
	a := &attention{app: app}
	app.Event.On("at:notify", func(e *application.CustomEvent) {
		data, _ := e.Data.(map[string]any)
		title, _ := data["title"].(string)
		body, _ := data["body"].(string)
		threadID, _ := data["threadId"].(string)
		if title == "" {
			return
		}
		// Native calls hop to the main thread; never block the event loop on them.
		go a.notify(title, body, threadID)
	})
	app.Event.On("at:badge", func(e *application.CustomEvent) {
		data, _ := e.Data.(map[string]any)
		count, _ := data["count"].(float64)
		a.mu.Lock()
		a.want = int(count)
		a.mu.Unlock()
		go a.applyBadge()
	})
}

func (a *attention) notifications() *notifications.NotificationService {
	a.notifyOnce.Do(func() {
		ns := notifications.New()
		if err := ns.ServiceStartup(context.Background(), application.ServiceOptions{}); err != nil {
			log.Printf("agenttunnel-desktop: notifications unavailable: %v", err)
			return
		}
		// Clicking a notification brings the window forward on that thread.
		ns.OnNotificationResponse(func(r notifications.NotificationResult) {
			if r.Error != nil {
				return
			}
			threadID, _ := r.Response.UserInfo["threadId"].(string)
			if threadID == "" {
				threadID = strings.TrimPrefix(r.Response.ID, notifyIDPrefix)
			}
			if w := a.app.Window.Current(); w != nil {
				w.Show()
				w.Focus()
			} else if all := a.app.Window.GetAll(); len(all) > 0 {
				all[0].Show()
				all[0].Focus()
			}
			if threadID != "" {
				a.app.Event.Emit("at:notification-open", map[string]any{"threadId": threadID})
			}
		})
		if ok, err := ns.CheckNotificationAuthorization(); err != nil || !ok {
			if ok, err := ns.RequestNotificationAuthorization(); err != nil || !ok {
				log.Printf("agenttunnel-desktop: notifications not authorized: %v", err)
				return
			}
		}
		a.notifier = ns
	})
	return a.notifier
}

// notifyIDPrefix keys a notification by thread, so a newer one for the same
// thread replaces the older instead of stacking.
const notifyIDPrefix = "agenttunnel-thread-"

func (a *attention) notify(title, body, threadID string) {
	ns := a.notifications()
	if ns == nil {
		return
	}
	err := ns.SendNotification(notifications.NotificationOptions{
		ID:       notifyIDPrefix + threadID,
		Title:    title,
		Body:     body,
		ThreadID: threadID,
		Data:     map[string]any{"threadId": threadID},
	})
	if err != nil {
		log.Printf("agenttunnel-desktop: send notification: %v", err)
	}
}

// applyBadge puts the newest requested count on the dock. Updates arrive on
// goroutines in any order; reading want under the lock makes the last request win.
func (a *attention) applyBadge() {
	a.badgeOnce.Do(func() {
		d := dock.New()
		if err := d.ServiceStartup(context.Background(), application.ServiceOptions{}); err != nil {
			log.Printf("agenttunnel-desktop: dock badge unavailable: %v", err)
			return
		}
		a.badger = d
	})
	if a.badger == nil {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	count := a.want
	if count == a.badge {
		return
	}
	var err error
	if count <= 0 {
		err = a.badger.RemoveBadge()
	} else {
		err = a.badger.SetBadge(strconv.Itoa(count))
	}
	if err == nil {
		a.badge = count
	}
}
