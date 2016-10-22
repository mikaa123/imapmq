// Package imapmq is an IMAP based message broker client.
package imapmq

import (
	"fmt"
	"log"
	"net/mail"
	"time"

	"github.com/mxk/go-imap/imap"
)

// Message type is an email. Queues (mailboxes) store messages. Messages will be
// passed when listening to a subscription channel, or when dequeuing.
type Message mail.Message

// Config holds configuration data to connect to the IMAP server.
type Config struct {
	Login, Pwd, URL string
}

// IMAPMQ is an imapmq broker client. It manages queues and coordinate low-level
// operations through a worker that performs operations requested by queues synchronously.
type IMAPMQ struct {
	queues map[string]*Queue
	jobs   chan<- job
	cfg    Config
	done   chan interface{}
}

// New creates a IMAPMQ instance. It initializes the instance's worker.
func New(cfg Config) (*IMAPMQ, error) {
	done := make(chan interface{})
	jobs, err := worker(cfg, done)
	if err != nil {
		return nil, err
	}
	queues := make(map[string]*Queue)
	mq := &IMAPMQ{queues, jobs, cfg, done}
	return mq, nil
}

// Queue creates or retrieve a `Queue` instance based on the mailbox name passed.
func (mq *IMAPMQ) Queue(mailbox string) (*Queue, error) {
	if q := mq.queues[mailbox]; q != nil {
		return q, nil
	}
	subs := make(map[string]chan *Message)
	q := &Queue{mailbox, subs, mq.jobs, mq.done}
	mq.queues[mailbox] = q
	return q, observer(mq, q)
}

// Logout disconnects the IMAPMQ client and releases its worker and observers.
func (mq *IMAPMQ) Logout() {
	close(mq.done)
}

// Queue represents a message queue based on a mailbox. A queue allows you
// to publish, subscribe or dequeue `Message` (emails).
type Queue struct {
	name string
	subs map[string]chan *Message
	jobs chan<- job
	done chan interface{}
}

// Pub publishes the `Message` for the queue. It appends a new email in the
// queue's mailbox by using the provided subject and body.
func (q *Queue) Pub(subject string, body []byte) error {
	mail := []byte(fmt.Sprintf("Subject: %s\n\n%s\n", subject, body))
	l := imap.NewLiteral(mail)
	select {
	case q.jobs <- &publishJob{q, l}:
	default:
		log.Panic("worker not ready")
	}
	return nil
}

// Sub adds a subscription to a subject on the queue. Use the returned channel
// to receive messages.
func (q *Queue) Sub(s string) <-chan *Message {
	if sub := q.subs[s]; sub != nil {
		return sub
	}
	sub := make(chan *Message)
	q.subs[s] = sub
	return sub
}

// Dequeue fetches and removes the oldest message from the queue. When no more
// messages are available, it returnes io.EOF.
func (q *Queue) Dequeue() (*Message, error) {
	c := make(chan *jobResult)
	q.jobs <- &dequeueJob{q, c}
	res := <-c
	close(c)
	return res.msg, res.err
}

// Selects the queue's mailbox
func (q *Queue) switchTo(c *imap.Client) error {
	if c.Mailbox != nil && c.Mailbox.Name == q.name {
		return nil
	}
	if _, err := c.Select(q.name, false); err != nil {
		return err
	}
	if _, err := imap.Wait(c.Check()); err != nil {
		return err
	}
	return nil
}

// Each queue spawns an observer. The observer listens to IMAP notifications
// (IDLE command) and notifies the subscribers by adding a new job to the worker.
func observer(mq *IMAPMQ, q *Queue) error {
	c, err := newIMAPClient(mq.cfg)
	if err != nil {
		return err
	}
	err = q.switchTo(c)
	if err != nil {
		return err
	}
	cmd, err := c.Idle()
	if err != nil {
		log.Fatal(err)
	}
	go func() {
		for cmd.InProgress() {
			c.Data = nil
			c.Recv(200 * time.Millisecond)
			if len(c.Data) != 0 && c.Data[0].Label == "EXISTS" {
				rsp := c.Data[0]
				select {
				case mq.jobs <- &notifyJob{q, rsp.Fields[0].(uint32)}:
				default:
				}
			}
			select {
			case <-mq.done:
				c.IdleTerm()
			default:
			}
		}
		c.Logout(30 * time.Second)
	}()
	return nil
}

// Creates a new logged-in IMAP client.
func newIMAPClient(cfg Config) (*imap.Client, error) {
	c, err := imap.DialTLS(cfg.URL, nil)
	if err != nil {
		return nil, err
	}
	_, err = c.Login(cfg.Login, cfg.Pwd)
	if err != nil {
		return nil, err
	}
	return c, nil
}
