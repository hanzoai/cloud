package order

import (
	"errors"
	"strings"
	"time"

	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/models/product"
)

var PendingReservationError = errors.New("Product is already being reserved.")
var AlreadyReservedError = errors.New("Product is reserved.")

func (o *Order) MakeReservations() error {
	for _, item := range o.Items {
		if err := o.Datastore().RunInTransaction(func(db *datastore.Datastore) error {
			p := product.New(db)

			if err := p.GetById(item.ProductId); err != nil {
				return err
			}

			if !p.Reservation.IsReservable {
				return nil
			}

			if p.Reservation.IsBeingReserved {
				return PendingReservationError
			}

			if p.Reservation.ReservedBy != "" {
				return AlreadyReservedError
			}

			p.Reservation.IsBeingReserved = true
			p.Reservation.OrderId = o.Id()
			p.Reservation.ReservedAt = time.Now()

			p.Reservation.ReservedBy = ""
			strs := strings.Fields(o.BillingAddress.Name)

			for _, str := range strs {
				p.Reservation.ReservedBy += strings.ToUpper(str[:1]) + "."
			}

			return p.Update()
		}, nil); err != nil {
			return err
		}
	}

	return nil
}

func (o *Order) CancelReservations() error {
	for _, item := range o.Items {
		if err := o.Datastore().RunInTransaction(func(db *datastore.Datastore) error {
			p := product.New(db)

			if err := p.GetById(item.ProductId); err != nil {
				return err
			}

			if !p.Reservation.IsReservable {
				return nil
			}

			if p.Reservation.OrderId != o.Id() {
				return nil
			}

			p.Reservation.IsBeingReserved = false
			p.Reservation.OrderId = ""
			p.Reservation.ReservedAt = time.Time{}
			p.Reservation.ReservedBy = ""

			return p.Update()
		}, nil); err != nil {
			return err
		}
	}

	return nil
}
