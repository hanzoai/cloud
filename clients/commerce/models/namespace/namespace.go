package namespace

import (
	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/log"
	"github.com/hanzoai/cloud/clients/commerce/models/mixin"
	"github.com/hanzoai/orm"
)

var kind = "namespace"


func init() { orm.Register[Namespace]("namespace") }

type Namespace struct {
	mixin.Model[Namespace]

	IntId int64
	Name  string
}

func (n *Namespace) NameExists(name string) (ok bool, err error) {
	n.RunInTransaction(func() error {
		_, ok, err = n.IdExists(name)
		return err
	}, nil)
	return ok, err
}

// Override put on model
func (n *Namespace) Put() (err error) {
	return n.RunInTransaction(func() error {
		// Set key
		n.SetKey(n.Name)

		// Check if namespace exists
		ok, err := n.Exists()
		if err != nil && err != datastore.ErrNoSuchEntity {
			log.Warn("Failed to check for existence of namespace: %v", err)
			return err
		}

		// Warn if it already exists, otherwise save.
		if ok {
			log.Warn("Namespace exists: %v", n.Name)
			return NamespaceExists
		} else {
			return n.Model.Put()
		}
	}, &datastore.TransactionOptions{XG: true})
}

func (n *Namespace) Defaults() {
}

func New(db *datastore.Datastore) *Namespace {
	n := new(Namespace)
	n.Init(db)
	n.Defaults()
	return n
}

func Query(db *datastore.Datastore) datastore.Query {
	return db.Query(kind)
}
