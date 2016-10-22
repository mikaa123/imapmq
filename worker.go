package imapmq

import (
	"bytes"
	"fmt"
	"io"
	"log"
	"net/mail"
	"strconv"
	"time"

	"github.com/mxk/go-imap/imap"
)

// The job interface defines the `exec` method which is implemented by all jobs.
type job interface {
	exec(*imap.Client)
}

type jobResult struct {
	msg *Message
	err error
}

// The dequeueJob represents a dequeue intent.
type dequeueJob struct {
	q *Queue
	c chan *jobResult
}

// Dequeuing use the Conditional Store IMAP extension (RFC4551) the prevent
// race conditions when multiple clients dequeue concurrently.
func (j *dequeueJob) exec(c *imap.Client) {
	err := j.q.switchTo(c)
	if err != nil {
		j.c <- &jobResult{nil, err}
		return
	}
	var msg *Message
	for {
		msg, err = dequeue(c, j.q)
		if err != nil && err != io.EOF {
			continue
		}
		break
	}
	j.c <- &jobResult{msg, err}
}

func dequeue(c *imap.Client, q *Queue) (*Message, error) {
	mail, info, err := fetchMail(c, 1)
	if err != nil {
		return nil, err
	}
	cmd, err := flagDelete(c, info)
	if err != nil {
		return nil, err
	}
	rsp, err := cmd.Result(imap.OK)
	if err != nil {
		return nil, err
	}
	if rsp.Label == "MODIFIED" {
		return nil, fmt.Errorf("Race condition")
	}
	return (*Message)(mail), nil
}

func flagDelete(c *imap.Client, info *imap.MessageInfo) (*imap.Command, error) {
	mseq := (info.Attrs["MODSEQ"]).([]imap.Field)[0]
	suid, _ := imap.NewSeqSet(strconv.Itoa(int(info.UID)))
	q := fmt.Sprintf("(UNCHANGEDSINCE %d) FLAGS", mseq)
	return c.UIDStore(suid, q, imap.NewFlagSet("\\Deleted"))
}

// The publishJob represents the intent of publishing a message to a topic in a
// queue. `Literal` holds the complete mail (subject and body.)
type publishJob struct {
	q       *Queue
	literal imap.Literal
}

func (j *publishJob) exec(c *imap.Client) {
	err := j.q.switchTo(c)
	if err != nil {
		log.Print(err)
		return
	}
	_, err = imap.Wait(c.Append(j.q.name, nil, nil, j.literal))
	if err != nil {
		log.Print(err)
		return
	}
}

// The notifyJob represents the intent of notifying subscribers of a new message.
type notifyJob struct {
	q     *Queue
	msgID uint32
}

func (j *notifyJob) exec(c *imap.Client) {
	err := j.q.switchTo(c)
	if err != nil {
		log.Print(err)
		return
	}
	mail, _, err := fetchMail(c, int(j.msgID))
	if err != nil {
		log.Println(err)
		return
	}
	t := mail.Header.Get("Subject")
	select {
	case j.q.subs[t] <- (*Message)(mail):
	default:
	}
}

// Fetches a mail requesting the correct headers, and returns a parsed `mail.Message`
// instance along with metadata.
func fetchMail(c *imap.Client, seq int) (*mail.Message, *imap.MessageInfo, error) {
	s, _ := imap.NewSeqSet(strconv.Itoa(seq))
	cmd, err := imap.Wait(c.Fetch(s, "RFC822 UID MODSEQ"))
	if err != nil {
		return nil, nil, err
	}
	d := cmd.Data[0]
	m, err := getMail(d)
	if err != nil {
		return nil, nil, err
	}
	return m, d.MessageInfo(), nil
}

// getMail builds a `mail.Message` from the response.
func getMail(rsp *imap.Response) (*mail.Message, error) {
	if rsp == nil {
		return nil, fmt.Errorf("parse error")
	}
	msgInfo := rsp.MessageInfo()
	if msgInfo == nil {
		return nil, fmt.Errorf("parse error")
	}
	msgField := msgInfo.Attrs["RFC822"]
	if msgField == nil {
		return nil, fmt.Errorf("parse error")
	}
	mailBytes := imap.AsBytes(msgField)
	return mail.ReadMessage(bytes.NewReader(mailBytes))
}

// A worker is associated to a IMAPMQ instance. It processes incoming jobs
// from all the different queues synchronously.
func worker(cfg Config, done <-chan interface{}) (chan<- job, error) {
	c, err := newIMAPClient(cfg)
	if err != nil {
		return nil, err
	}
	jobs := make(chan job)
	go func() {
		defer func() {
			close(jobs)
			c.Logout(30 * time.Second)
		}()
		for {
			select {
			case j := <-jobs:
				j.exec(c)
			case <-done:
				return
			}
		}
	}()
	return jobs, nil
}
