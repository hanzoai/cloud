package tokentransaction

import (
	"github.com/hanzoai/cloud/clients/commerce/models/mixin"
	"time"
)

// Datastructure for Bitcoin Transaction
type Transaction struct {
	mixin.BaseModel

	Amount           float64   `json:"amount"`
	Fees             float64   `json:"fees"`
	Timestamp        time.Time `json:"timestamp"`
	SendingAddress   string    `json:"sendingAddress"`
	ReceivingAddress string    `json:"receivingAddress"`
	SendingName      string    `json:"sendingName"`
	SendingUserId    string    `json:"sendingUserId"`
	SendingState     string    `json:"sendingState"`
	SendingCountry   string    `json:"sendingCountry"`
	ReceivingName    string    `json:"receivingName"`
	ReceivingUserId  string    `json:"receivingUserId"`
	ReceivingState   string    `json:"receivingState"`
	ReceivingCountry string    `json:"receivingCountry"`
	SenderFlagged    bool      `json:"senderFlagged"`
	ReceiverFlagged  bool      `json:"receiverFlagged"`
	Protocol         string    `json:"protocol"`
	TransactionHash  string    `json:"transactionHash"`
}
