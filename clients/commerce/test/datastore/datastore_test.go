package test

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/datastore/query"
	"github.com/hanzoai/cloud/clients/commerce/db"
	"github.com/hanzoai/cloud/clients/commerce/log"
)

func Test(t *testing.T) {
	log.SetVerbose(testing.Verbose())
	RegisterFailHandler(Fail)
	RunSpecs(t, "datastore")
}

var (
	testDB  db.DB
	testMgr *db.Manager
	tempDir string
	ds      *datastore.Datastore
)

// Setup SQLite directly — no ae dependency
var _ = BeforeSuite(func() {
	var err error
	tempDir, err = os.MkdirTemp("", "datastore-test-*")
	Expect(err).NotTo(HaveOccurred())

	cfg := db.DefaultConfig()
	cfg.DataDir = tempDir
	cfg.OrgDataDir = filepath.Join(tempDir, "orgs")
	cfg.UserDataDir = filepath.Join(tempDir, "users")
	cfg.EnableDatastore = false
	cfg.EnableVectorSearch = false

	testMgr, err = db.NewManager(cfg)
	Expect(err).NotTo(HaveOccurred())

	testDB, err = testMgr.Org("test")
	Expect(err).NotTo(HaveOccurred())

	datastore.SetDefaultDB(testDB)
	query.SetDefaultDB(testDB)

	ds = datastore.New(context.Background())
})

var _ = AfterSuite(func() {
	if testMgr != nil {
		testMgr.Close()
	}
	if tempDir != "" {
		os.RemoveAll(tempDir)
	}
})

var _ = Describe("Key", func() {
	Context("AllocateID", func() {
		It("should be non-zero", func() {
			id := ds.AllocateID("test", nil)
			Expect(id).NotTo(Equal(0))
		})
	})

	Context("Integer key", func() {
		It("should create key from int", func() {
			ickey := ds.NewIncompleteKey("foo", nil)
			aekey, _ := ds.NewKeyFromInt("foo", 10, nil)
			Expect(aekey).NotTo(Equal(ickey))
		})
	})

	Context("String key", func() {
		It("should create key from string", func() {
			ickey := ds.NewIncompleteKey("foo", nil)
			aekey := ds.NewKeyFromString("foo", "bar", nil)
			Expect(aekey).NotTo(Equal(ickey))
		})
	})
})

var _ = Describe("Datastore.DecodeKey", func() {
	Context("Key encoded with datastore (string key)", func() {
		It("should preserve the string ID", func() {
			key := ds.NewKeyFromString("decodekey-test", "decodekey-testkey", nil)
			decodedKey, err := ds.DecodeKey(key.Encode())
			Expect(err).ToNot(HaveOccurred())
			Expect(decodedKey.StringID()).To(Equal(key.StringID()))
		})
	})

	Context("Key encoded with datastore (integer key via EncodeKey)", func() {
		It("should preserve kind and intID", func() {
			key := ds.NewKey("user", "", 42, nil)
			encoded := ds.EncodeKey(key)
			decodedKey, err := ds.DecodeKey(encoded)
			Expect(err).ToNot(HaveOccurred())
			Expect(decodedKey.Kind()).To(Equal(key.Kind()))
			Expect(decodedKey.IntID()).To(Equal(key.IntID()))
		})
	})
})

type Entity struct {
	Field string
}

func str(i int) string {
	return strconv.Itoa(i)
}

var _ = Describe("Datastore.Get", func() {
	entity := &Entity{"test-get-field"}
	kind := "test-get"
	var key datastore.Key

	Context("When storing entity with Datastore.Put", func() {
		BeforeEach(func() {
			var err error
			key, err = ds.Put(kind, entity)
			Expect(err).NotTo(HaveOccurred())
		})

		It("Retrieved entity should not be empty", func() {
			retrievedEntity := &Entity{}
			err := ds.Get(key, retrievedEntity)
			Expect(err).ToNot(HaveOccurred())
			Expect(retrievedEntity).ToNot(BeZero())
		})

		It("Retrieved entity should equal what was inserted", func() {
			retrievedEntity := &Entity{}
			err := ds.Get(key, retrievedEntity)
			Expect(err).ToNot(HaveOccurred())
			Expect(retrievedEntity).To(Equal(entity))
		})
	})

	Context("When storing entity with Put and GetById", func() {
		retrievedEntity := &Entity{}
		BeforeEach(func() {
			key := ds.NewKeyFromString(kind, "key", nil)
			_, err := ds.Put(key, entity)
			Expect(err).ToNot(HaveOccurred())

			err = ds.GetById(key.Encode(), retrievedEntity)
			Expect(err).ToNot(HaveOccurred())
		})

		It("Retrieved entity should not be empty", func() {
			Expect(retrievedEntity).ToNot(BeZero())
		})

		It("Retrieved entity should equal what was inserted", func() {
			Expect(retrievedEntity).To(Equal(entity))
		})
	})
})

var _ = Describe("Put", func() {
	kind := "test-put"

	Context("With Datastore.Get", func() {
		It("should store entity successfully", func() {
			a := &Entity{"test-wrapper-put"}
			b := &Entity{}

			key, err := ds.Put(kind, a)
			Expect(err).NotTo(HaveOccurred())

			err = ds.Get(key, b)
			Expect(err).NotTo(HaveOccurred())

			Expect(a).To(Equal(b))
		})
	})

	Context("With Get after Put", func() {
		It("should be the same", func() {
			a := &Entity{"test-put-get"}
			b := &Entity{}

			key, err := ds.Put(kind, a)
			Expect(err).NotTo(HaveOccurred())

			err = ds.Get(key, b)
			Expect(err).NotTo(HaveOccurred())
			Expect(a).To(Equal(b))
		})
	})

	Context("With the query api", func() {
		It("should be the same", func() {
			a := &Entity{"test-query-put"}
			b := &Entity{}
			_, err := ds.Put(kind, a)
			Expect(err).NotTo(HaveOccurred())
			_, err = ds.Query(kind).
				Filter("Field =", a.Field).
				Run().
				Next(b)

			Expect(err).NotTo(HaveOccurred())
			Expect(a).To(Equal(b))
		})
	})
})

var _ = Describe("Datastore.GetMulti", func() {
	kind := "datastore-getmulti-test"

	Context("With Put and GetMulti", func() {
		It("should be the same", func() {
			a := make([]Entity, 10)
			keys := make([]datastore.Key, len(a))
			for i := range keys {
				a[i].Field = str(i)
				key := ds.NewKeyFromString(kind, str(i), nil)
				keys[i] = key
				_, err := ds.Put(key, &a[i])
				Expect(err).ToNot(HaveOccurred())
			}

			b := make([]Entity, 10)
			err := ds.GetMulti(keys, b)
			Expect(err).ToNot(HaveOccurred())
			Expect(b).To(Equal(a))
			Expect(b).To(HaveLen(len(a)))
		})
	})
})
