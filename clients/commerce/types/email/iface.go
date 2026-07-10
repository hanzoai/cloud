package email

type Sender interface {
	Send(message *Message) error
}

type Marketer interface {
	Subscribe(l *List, s *Subscriber) error
}
