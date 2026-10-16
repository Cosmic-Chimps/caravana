package deploy

import (
	"context"
	"fmt"
	"maps"
	"strings"
)

// Notification channels (feature 018). Like the backup destination's key and
// the registries' passwords, the secret parts of a channel live in a Nomad
// Variable of Caravana's own (constitution VII), never in SQLite.

// notificationVariablePrefix is where the secrets are kept, one variable per
// channel.
const notificationVariablePrefix = "caravana/notifications/"

// NotificationVariablePath is the Nomad Variable that holds the secrets of the
// channel with this ID: "caravana/notifications/nch_0123456789ab". Its items
// depend on the kind of channel: "url" for Slack and Discord, "token" for ntfy
// when it has one, and "signing_secret" for a webhook.
func NotificationVariablePath(channelID string) string {
	return notificationVariablePrefix + channelID
}

// checkNotificationID refuses an ID that could change which variable the path
// names. Channel IDs are made by Caravana ("nch_" and hex), so this guards
// against a bug rather than against input.
func checkNotificationID(channelID string) error {
	if channelID == "" || strings.Trim(channelID, "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789_-") != "" {
		return fmt.Errorf("notification channel ID %q is not a valid variable name", channelID)
	}
	return nil
}

// NotificationSecret returns the secret items stored for a channel. A channel
// with none (an ntfy topic without a token) gets an empty map and no error.
// Errors name the action, never the values.
func (d *Deployer) NotificationSecret(ctx context.Context, channelID string) (map[string]string, error) {
	if err := checkNotificationID(channelID); err != nil {
		return nil, err
	}
	items, found, err := d.readVariableIfAny(ctx, NotificationVariablePath(channelID))
	if err != nil {
		return nil, err // names the variable already
	}
	if !found {
		return map[string]string{}, nil
	}
	return items, nil
}

// SaveNotificationSecret keeps items as the channel's secrets, replacing
// whatever was stored before. Items with an empty value are left out, and when
// none is left the variable is deleted instead, because Nomad refuses a
// variable whose items are all empty.
func (d *Deployer) SaveNotificationSecret(ctx context.Context, channelID string, items map[string]string) error {
	if err := checkNotificationID(channelID); err != nil {
		return err
	}
	kept := maps.Clone(items)
	maps.DeleteFunc(kept, func(_, v string) bool { return v == "" })
	if len(kept) == 0 {
		return d.deleteVariable(ctx, NotificationVariablePath(channelID))
	}
	return d.writeVariable(ctx, NotificationVariablePath(channelID), kept)
}

// DeleteNotificationSecret forgets a channel's secrets. A channel with none is
// not an error.
func (d *Deployer) DeleteNotificationSecret(ctx context.Context, channelID string) error {
	if err := checkNotificationID(channelID); err != nil {
		return err
	}
	return d.deleteVariable(ctx, NotificationVariablePath(channelID))
}
