package requests

var PartialRefund = `
{
	"amount": 123
}`

var NegativeRefund = `
{
	"amount": -1
}
`

var LargeRefundAmount = `
{
	"amount": 9999999999
}
`
