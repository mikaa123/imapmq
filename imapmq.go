// Package imapmq is an IMAP based message broker client.
package imapmq

import (
	"crypto/tls"
	"fmt"
	"log"
	"net"
	"net/mail"
	"time"

	"github.com/mxk/go-imap/imap"
)

// Message type is an email. Queues (mailboxes) store messages. Messages will be
// passed when listening to a subscription channel, or when dequeuing.
type Message mail.Message

// Config holds configuration data to connect to the IMAP server.
type Config struct {
	Login, Passwd, URL string
}

// IMAPMQ is an imapmq broker client. It manages queues and coordinate low-level
// operations through a worker that performs operations requested by queues synchronously.
type IMAPMQ struct {
	queues map[string]*Queue
	jobs   chan<- job
	cfg    Config
	done   chan interface{}
	errs   chan error // Any error will be sent there.
}

// New creates a IMAPMQ instance. It initializes the instance's worker.
func New(cfg Config) (*IMAPMQ, error) {
	done := make(chan interface{})
	jobs, err := worker(cfg, done)
	if err != nil {
		return nil, err
	}
	queues := make(map[string]*Queue)
	errs := make(chan error)
	mq := &IMAPMQ{queues, jobs, cfg, done, errs}
	return mq, nil
}

// Queue creates or retrieve a `Queue` instance based on the mailbox name passed.
func (mq *IMAPMQ) Queue(mailbox string) (*Queue, error) {
	if q := mq.queues[mailbox]; q != nil {
		return q, nil
	}
	subs := make(map[string]chan *Message)
	q := &Queue{mailbox, subs, mq}
	mq.queues[mailbox] = q
	return q, observer(q)
}

// Errs return the error channel. Any asynchronous task will send an error down
// this channel.
func (mq *IMAPMQ) Errs() <-chan error {
	return mq.errs
}

// Sends an error to the errs channel, without blocking.
func (mq *IMAPMQ) handleErr(err error) {
	select {
	case mq.errs <- err:
	default:
	}
}

// Close disconnects the IMAPMQ client and releases its worker and observers.
func (mq *IMAPMQ) Close() {
	close(mq.done)
}

// Queue represents a message queue based on a mailbox. A queue allows you
// to publish, subscribe or dequeue `Message` (emails).
type Queue struct {
	name string
	subs map[string]chan *Message
	mq   *IMAPMQ
}

// Pub publishes the `Message` to the queue. It appends a new email in the
// queue's mailbox by using the provided subject and body.
func (q *Queue) Pub(subject string, body []byte) {
	mail := []byte(fmt.Sprintf("Subject: %s\n\n%s\n", subject, body))
	l := imap.NewLiteral(mail)
	if q.mq.jobs == nil {
		log.Panic("jobs queue is nil")
	}
	q.mq.jobs <- &publishJob{q, l}
}

// Sub adds a subscription to a subject on the queue. Use "*" to receive every
// new message from the queue. Returns a channel where the messages will be passed.
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
	q.mq.jobs <- &dequeueJob{q, c}
	res := <-c
	close(c)
	return res.msg, res.err
}

// Selects the queue's mailbox and refreshes it if `check` is true.
func (q *Queue) switchTo(c *imap.Client, check bool) error {
	if c.Mailbox == nil || (c.Mailbox != nil && c.Mailbox.Name != q.name) {
		if _, err := c.Select(q.name, false); err != nil {
			return err
		}
	}
	if !check {
		return nil
	}
	if _, err := imap.Wait(c.Check()); err != nil {
		return err
	}
	return nil
}

// Each queue spawns an observer. The observer listens to IMAP notifications
// (IDLE command) and notifies the subscribers by adding a new job to the worker.
func observer(q *Queue) error {
	c, err := newIMAPClient(q.mq.cfg)
	if err != nil {
		return err
	}
	_, err = c.Select(q.name, false)
	if err != nil {
		return err
	}
	cmd, err := c.Idle()
	if err != nil {
		return err
	}
	go func() {
		for cmd.InProgress() {
			c.Data = nil
			c.Recv(200 * time.Millisecond)
			if len(c.Data) != 0 && c.Data[0].Label == "EXISTS" {
				rsp := c.Data[0]
				if q.mq.jobs != nil {
					select {
					case q.mq.jobs <- &notifyJob{q, rsp.Fields[0].(uint32)}:
					case <-q.mq.done:
						c.IdleTerm()
						break
					}
				}
			}
			select {
			case <-q.mq.done:
				c.IdleTerm()
				break
			default:
			}
		}
		c.Logout(30 * time.Second)
	}()
	return nil
}

// dialTLS connects to the imap server using TLS. It is based on imap.DialTLS
// with the difference that here, we set keep-alive on the tcp socket to make
// sure it doesn't won't break whem the client is IDLE.
func dialTLS(addr string) (c *imap.Client, err error) {
	addr = net.JoinHostPort(addr, "993")
	conn, err := net.DialTimeout("tcp", addr, 30*time.Second)
	if tcpConn, ok := conn.(*net.TCPConn); ok {
		tcpConn.SetKeepAlive(true)
		tcpConn.SetKeepAlivePeriod(30 * time.Second)
	}
	if err == nil {
		host, _, _ := net.SplitHostPort(addr)
		tlsConn := tls.Client(conn, &tls.Config{ServerName: host})
		if c, err = imap.NewClient(tlsConn, host, 60*time.Second); err != nil {
			conn.Close()
		}
	}
	return
}

// Creates a new logged-in IMAP client.
func newIMAPClient(cfg Config) (*imap.Client, error) {
	c, err := dialTLS(cfg.URL)
	if err != nil {
		return nil, err
	}
	_, err = c.Login(cfg.Login, cfg.Passwd)
	if err != nil {
		return nil, err
	}
	return c, nil
}
