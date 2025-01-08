package broadcast

import (
	"context"
	"errors"
	"time"

	pb "github.com/nginx/agent/v3/api/grpc/mpi/v1"
)

// Broadcaster defines an interface for consumers to subscribe to File updates.
type Broadcaster interface {
	Subscribe() (chan FileOverviewMessage, chan error)
	Send(FileOverviewMessage) error
	CancelSubscription(chan FileOverviewMessage)
}

type subscriberChannels struct {
	listenCh   chan FileOverviewMessage
	responseCh chan error
}

// DeploymentBroadcaster sends out a signal when an nginx Deployment has updated
// configuration files. The signal is received by any agent Subscription that cares
// about this Deployment. The agent Subscription will then send a response of whether or not
// the configuration was successfully applied.
type DeploymentBroadcaster struct {
	publishCh chan FileOverviewMessage
	subCh     chan subscriberChannels
	unsubCh   chan chan FileOverviewMessage
	listeners map[chan FileOverviewMessage]chan error
}

// NewDeploymentBroadcaster returns a new instance of a DeploymentBroadcaster.
func NewDeploymentBroadcaster(ctx context.Context) *DeploymentBroadcaster {
	broadcaster := &DeploymentBroadcaster{
		listeners: make(map[chan FileOverviewMessage]chan error),
		publishCh: make(chan FileOverviewMessage),
		subCh:     make(chan subscriberChannels),
		unsubCh:   make(chan chan FileOverviewMessage),
	}
	go broadcaster.run(ctx)

	return broadcaster
}

// Subscribe allows a listener to subscribe to broadcast messages. It returns the channel
// to listen on for messages, as well as a channel to respond on.
func (b *DeploymentBroadcaster) Subscribe() (chan FileOverviewMessage, chan error) {
	listenCh := make(chan FileOverviewMessage)
	responseCh := make(chan error)
	sc := subscriberChannels{
		listenCh:   listenCh,
		responseCh: responseCh,
	}

	b.subCh <- sc
	return listenCh, responseCh
}

// Send the fileOverviews to all listeners. Wait for a response from each listener
// or until a timeout.
func (b *DeploymentBroadcaster) Send(fileOverviews FileOverviewMessage) error {
	b.publishCh <- fileOverviews

	timeout := 15 * time.Second
	timeoutErr := errors.New("timeout waiting for data plane response")

	done := make(chan error, len(b.listeners))

	for _, responseCh := range b.listeners {
		go func(ch chan error) {
			select {
			case res := <-ch:
				done <- res
			case <-time.After(timeout):
				done <- timeoutErr
			}
		}(responseCh)
	}

	var err error
	for range len(b.listeners) {
		select {
		case res := <-done:
			err = errors.Join(err, res)
		case <-time.After(timeout):
			err = errors.Join(err, timeoutErr)
		}
	}

	return err
}

// CancelSubscription removes a Subscriber from the channel list.
func (b *DeploymentBroadcaster) CancelSubscription(channel chan FileOverviewMessage) {
	b.unsubCh <- channel
}

// run starts the broadcaster loop. It handles the following events:
// - if context is canceled, return.
// - if receiving a new subscriber, add it to the subscriber list.
// - if receiving a canceled subscription, remove it from the subscriber list.
// - if receiving a message to publish, send it to all subscribers.
func (b *DeploymentBroadcaster) run(ctx context.Context) {
	defer func() {
		for listener := range b.listeners {
			if listener != nil {
				close(listener)
			}
		}
	}()

	for {
		select {
		case <-ctx.Done():
			return
		case channels := <-b.subCh:
			b.listeners[channels.listenCh] = channels.responseCh
		case msgCh := <-b.unsubCh:
			delete(b.listeners, msgCh)
		case msg := <-b.publishCh:
			for msgCh := range b.listeners {
				select {
				case msgCh <- msg:
				default:
				}
			}
		}
	}
}

// FileOverviewMessage is sent to all subscribers to send to the nginx agents for a ConfigApplyRequest.
type FileOverviewMessage struct {
	// ConfigVersion is the hashed configuration version of the included files.
	ConfigVersion string
	// FileOverviews contain the overviews of all files to be sent.
	FileOverviews []*pb.File
}
