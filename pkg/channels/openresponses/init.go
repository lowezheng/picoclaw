package openresponses

import (
	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/channels"
	"github.com/sipeed/picoclaw/pkg/config"
)

func init() {
	channels.RegisterSafeFactory(
		config.ChannelOpenResponses,
		func(bc *config.Channel, settings *config.OpenResponsesSettings, b *bus.MessageBus) (channels.Channel, error) {
			ch, err := NewOpenResponsesChannel(bc, settings, b)
			if err != nil {
				return nil, err
			}
			if bc.Name() != config.ChannelOpenResponses {
				ch.SetName(bc.Name())
			}
			return ch, nil
		},
	)
}
