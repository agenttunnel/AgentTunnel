package transport

import (
	"context"
)

// RemoteItem is one connectivity row in the desktop's Remote section. State is
// one of off, connecting, pairing, connected, or error; QR is set only while a
// linked-device pairing is waiting to be scanned. Unavailable marks a surface
// this build cannot connect at all, so the UI must not offer a toggle.
type RemoteItem struct {
	ID          string `json:"id"` // "phone" | "whatsapp"
	Label       string `json:"label"`
	State       string `json:"state"`
	Detail      string `json:"detail,omitempty"`
	QR          string `json:"qr,omitempty"`
	Unavailable bool   `json:"unavailable,omitempty"`
}

// RemoteStatus is the Remote section's whole payload.
type RemoteStatus struct {
	Items []RemoteItem `json:"items"`
}

// RemoteControl is the WhatsApp bridge as the RPC surface drives it. Nil means
// the daemon has no bridge, which reports the item as off.
type RemoteControl interface {
	// Status reports the WhatsApp item. The dispatcher owns its id and label.
	Status() RemoteItem
	Connect(ctx context.Context) error
	Disconnect() error
}

// RemoteStatus composes the section from the phone relay state and the WhatsApp
// bridge. The phone row is derived, never stored.
func (d *Dispatcher) RemoteStatus() RemoteStatus {
	return RemoteStatus{Items: []RemoteItem{
		phoneRemoteItem(d.relayControl()),
		d.whatsappRemoteItem(),
	}}
}

// whatsappRemoteItem reads the bridge, filling in the row's identity so the
// bridge only reports state.
func (d *Dispatcher) whatsappRemoteItem() RemoteItem {
	if d.remote == nil {
		return RemoteItem{ID: "whatsapp", Label: "WhatsApp", State: "off", Detail: "Unavailable", Unavailable: true}
	}
	item := d.remote.Status()
	item.ID = "whatsapp"
	item.Label = "WhatsApp"
	if item.State == "" {
		item.State = "off"
	}
	return item
}

// phoneRemoteItem derives the phone row from the existing relay tunnel: phones
// reach this daemon either through the in-app tunnel's LAN address or through
// the configured relay.
func phoneRemoteItem(rc RelayControl) RemoteItem {
	item := RemoteItem{ID: "phone", Label: "Phone"}
	tunnel := rc.TunnelStatus()
	relay := rc.Status()
	switch {
	case tunnel.Listening:
		item.State = "connected"
		item.Detail = "Paired phones can reach this daemon"
		if tunnel.LANURL != "" {
			item.Detail = "Reachable at " + tunnel.LANURL
		}
	case relay.Enabled && relay.Connected:
		item.State = "connected"
		item.Detail = "Connected through the relay"
	case relay.Enabled:
		item.State = "connecting"
		item.Detail = "Relay enabled, waiting for a connection"
	default:
		item.State = "off"
		item.Detail = "No phone paired"
	}
	return item
}

// SetRemoteNotifier installs the broadcast the daemon uses to push status
// changes to every client. Call it after the server exists.
func (d *Dispatcher) SetRemoteNotifier(f func(RemoteStatus)) { d.remoteNotifier = f }

// BroadcastRemoteStatus pushes the current section to connected clients. It is
// the bridge's change hook, and RPCs call it too so a synchronous transition is
// never missed.
func (d *Dispatcher) BroadcastRemoteStatus() {
	if d.remoteNotifier == nil {
		return
	}
	d.remoteNotifier(d.RemoteStatus())
}
