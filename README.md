![](https://cloud.githubusercontent.com/assets/428280/19731868/1de9d80c-9b9f-11e6-8faf-fa6f4e8ea6de.png)

IMAPMQ is an IMAP based **message broker client**. It provides a simple interface
for publishing, subscribing and dequeuing messages. It also supports concurrent
access to the same message queue.

## How it works
IMAPMQ treats IMAP mailboxes as queues. In order to add a message to a queue,
IMAPMQ appends an email to the mailbox.

**Features**
- IMAPMQ can connect to any IMAPv4rev1 server with the CONDSTORE extension: simply create a GMail account
- Publish/Subscribe
- Message Queue
- Message format agnostic
- No polling, the IMAP server notifies the client of new messages thanks to the IDLE command
- Concurrency aware: multiple dequeuing instances can work on the same queue
- Bring your own GUI: any IMAP client would do

## Example: A simple chat
The following example connects to an IMAP account, and creates a queue based on the INBOX mailbox.
It spawns a goroutine that subscribes to the "chat" topic and listens to the returned channel.
Anytime a user writes something and press enter, a new "chat" message is published to the queue.

_You need to have an IMAP server to connect to. If you don't, you can create a GMAIL account.
Make sure you enable IMAP (more info here ()[https://support.google.com/mail/answer/7126229?hl=en]) if you do._

~~~~go
package main

import (
	"bufio"
	"log"
	"os"

	"github.com/mikaa123/imapmq"
)

func main() {
	// Create a new IMAPMQ client
	mq, err := imapmq.New(imapmq.Config{
		Login: "login",
		Pwd:   "password",
		URL:   "imap.gmail.com",
	})
	if err != nil {
		log.Panic(err)
	}
	defer mq.Logout()

	// Create a queue based on INBOX
	q, err := mq.Queue("INBOX")
	if err != nil {
		log.Panic(err)
	}

	go func() {
		// Subscribe to messages with the "chat" subject
		c := q.Sub("chat")
		for msg := range c { // msg is a mail.Message instance.
			log.Printf("%s", msg.Header.Get("Subject"))
		}
	}()

	// We scan stdin for user input
	scanner := bufio.NewScanner(os.Stdin)
	for scanner.Scan() {
		// Publish a message with the "chat" subject in the INBOX queue
		q.Pub("chat", []byte(scanner.Text()))
	}
}
~~~~

## License
MIT © Michael Sokol
