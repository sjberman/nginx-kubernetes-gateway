package broadcast

import (
	"context"
	"errors"
	"sync"

	pb "github.com/nginx/agent/v3/api/grpc/mpi/v1"
	"k8s.io/apimachinery/pkg/util/uuid"
)

//go:generate go run github.com/maxbrunsfeld/counterfeiter/v6 -generate

//counterfeiter:generate . Broadcaster

// Broadcaster defines an interface for consumers to subscribe to File updates.
type Broadcaster interface {
	Subscribe() SubscriberChannels
	Send(NginxAgentMessage) (bool, error)
	CancelSubscription(string)
}

type SubscriberChannels struct {
	ListenCh   <-chan NginxAgentMessage
	ResponseCh chan<- error
	ID         string
}

type storedChannels struct {
	listenCh   chan<- NginxAgentMessage
	responseCh <-chan error
	id         string
}

// DeploymentBroadcaster sends out a signal when an nginx Deployment has updated
// configuration files. The signal is received by any agent Subscription that cares
// about this Deployment. The agent Subscription will then send a response of whether or not
// the configuration was successfully applied.
type DeploymentBroadcaster struct {
	publishCh chan NginxAgentMessage
	subCh     chan storedChannels
	unsubCh   chan string
	listeners map[string]storedChannels
	errorCh   chan error
}

// NewDeploymentBroadcaster returns a new instance of a DeploymentBroadcaster.
func NewDeploymentBroadcaster(ctx context.Context) *DeploymentBroadcaster {
	broadcaster := &DeploymentBroadcaster{
		listeners: make(map[string]storedChannels),
		publishCh: make(chan NginxAgentMessage),
		subCh:     make(chan storedChannels),
		unsubCh:   make(chan string),
		errorCh:   make(chan error),
	}
	go broadcaster.run(ctx)

	return broadcaster
}

// Subscribe allows a listener to subscribe to broadcast messages. It returns the channel
// to listen on for messages, as well as a channel to respond on.
func (b *DeploymentBroadcaster) Subscribe() SubscriberChannels {
	listenCh := make(chan NginxAgentMessage)
	responseCh := make(chan error)
	id := string(uuid.NewUUID())

	subscriberChans := SubscriberChannels{
		ID:         id,
		ListenCh:   listenCh,
		ResponseCh: responseCh,
	}
	storedChans := storedChannels{
		id:         id,
		listenCh:   listenCh,
		responseCh: responseCh,
	}

	b.subCh <- storedChans
	return subscriberChans
}

// Send the message to all listeners. Wait for all listeners to respond.
// Returns true if there were listeners that received the message, and returns any
// responses (nil for success, error for failure).
func (b *DeploymentBroadcaster) Send(message NginxAgentMessage) (bool, error) {
	b.publishCh <- message

	return len(b.listeners) > 0, <-b.errorCh
}

// CancelSubscription removes a Subscriber from the channel list.
func (b *DeploymentBroadcaster) CancelSubscription(id string) {
	b.unsubCh <- id
}

// run starts the broadcaster loop. It handles the following events:
// - if context is canceled, return.
// - if receiving a new subscriber, add it to the subscriber list.
// - if receiving a canceled subscription, remove it from the subscriber list.
// - if receiving a message to publish, send it to all subscribers.
func (b *DeploymentBroadcaster) run(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case channels := <-b.subCh:
			b.listeners[channels.id] = channels
		case id := <-b.unsubCh:
			delete(b.listeners, id)
		case msg := <-b.publishCh:
			var wg sync.WaitGroup
			wg.Add(len(b.listeners))

			responses := make(chan error, len(b.listeners))
			for _, channels := range b.listeners {
				go func() {
					defer wg.Done()

					// send message and wait for it to be read
					channels.listenCh <- msg
					// wait for response
					res := <-channels.responseCh
					// add response to the list of responses
					responses <- res
				}()
			}
			wg.Wait()

			var err error
			for range len(b.listeners) {
				err = errors.Join(err, <-responses)
			}
			b.errorCh <- err
		}
	}
}

// MessageType is the type of message to be sent.
type MessageType int

const (
	// ConfigApplyRequest sends files to update nginx configuration.
	ConfigApplyRequest MessageType = iota
	// APIRequest sends an NGINX Plus API request to update configuration.
	APIRequest
)

// NginxAgentMessage is sent to all subscribers to send to the nginx agents for either a ConfigApplyRequest
// or an APIActionRequest.
type NginxAgentMessage struct {
	// ConfigVersion is the hashed configuration version of the included files.
	ConfigVersion string
	// NGINXPlusAction is an NGINX Plus API action to be sent.
	NGINXPlusAction *pb.NGINXPlusAction
	// FileOverviews contain the overviews of all files to be sent.
	FileOverviews []*pb.File
	// Type defines the type of message to be sent.
	Type MessageType
}
