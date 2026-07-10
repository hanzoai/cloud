package counter

import (
	"context"
	"fmt"
	"math/rand"
	"time"

	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/datastore/key"
	"github.com/hanzoai/cloud/clients/commerce/delay"
	"github.com/hanzoai/cloud/clients/commerce/log"
	"github.com/hanzoai/cloud/clients/commerce/util/cache"
	"github.com/hanzoai/cloud/clients/commerce/util/json"
)

type counterConfig struct {
	Shards int
}

type Period string

const (
	None    Period = "none"
	Total   Period = "total"
	Hourly  Period = "hourly"
	Daily   Period = "daily"
	Weekly  Period = "weekly"
	Monthly Period = "monthly"
	Yearly  Period = "yearly"
)

type Shard struct {
	Name string `json:"name"`
	Tag  string `json:"tag"`
	// Counter
	Count   int    `json:"count"`
	StoreId string `json:"storeId"`
	Geo     string `json:"geo"`
	// Array
	Set  map[string]bool `datastore:"-"`
	Set_ string          `datastore:",noindex" json:"set"`

	Period Period `json:"period"`

	Time time.Time `json:"time"`
}

func (s *Shard) Load(ps []datastore.Property) (err error) {
	// Load supported properties
	if err = datastore.LoadStruct(s, ps); err != nil {
		return err
	}

	// Deserialize from datastore
	if len(s.Set_) > 0 {
		err = json.DecodeBytes([]byte(s.Set_), &s.Set)
	}

	return err
}

func (s *Shard) Save() (ps []datastore.Property, err error) {
	// Serialize unsupported properties
	s.Set_ = string(json.EncodeBytes(&s.Set))

	// Save properties
	return datastore.SaveStruct(s)
}

const (
	DefaultShards = 3
	ConfigKind    = "_counterconfig"
	ShardKind     = "_countershard"
)

func memcacheKey(name string) string {
	return ShardKind + ":" + name
}

func MemberExists(c context.Context, name string, value string) bool {
	members, err := Members(c, name)
	if err != nil {
		return false
	}
	for _, member := range members {
		if member == value {
			return true
		}
	}

	return false
}

func Members(c context.Context, name string) ([]string, error) {
	set := make(map[string]bool)
	members := make([]string, 0)
	mkey := memcacheKey(name)
	if _, err := cache.JSON.Get(c, mkey, &members); err == nil {
		return members, nil
	}

	db := datastore.New(c)
	q := db.Query(ShardKind).Filter("Name =", name)
	shards := []Shard{}
	if _, err := q.GetAll(&shards); err != nil {
		return members, err
	}

	for _, s := range shards {
		if s.Set == nil {
			continue
		}
		for member := range s.Set {
			set[member] = true
		}
	}

	for member := range set {
		members = append(members, member)
	}

	cache.JSON.Set(c, &cache.Item{
		Key:        mkey,
		Object:     &members,
		Expiration: 60 * time.Second,
	})
	return members, nil
}

// Count retrieves the value of the named counter.
func Count(c context.Context, name string) (int, error) {
	total := 0
	mkey := memcacheKey(name)
	if _, err := cache.JSON.Get(c, mkey, &total); err == nil {
		return total, nil
	}

	db := datastore.New(c)
	q := db.Query(ShardKind).Filter("Name =", name)
	shards := []Shard{}
	if _, err := q.GetAll(&shards); err != nil {
		return total, err
	}

	for _, s := range shards {
		total += s.Count
	}

	cache.JSON.Set(c, &cache.Item{
		Key:        mkey,
		Object:     &total,
		Expiration: 60 * time.Second,
	})
	return total, nil
}

// Adds a member to the array if it does not exist
func AddSetMember(c context.Context, name, tag, storeId, geo string, p Period, value string, t time.Time) error {
	if MemberExists(c, name, value) {
		return nil
	}
	return AddMember(c, name, tag, storeId, geo, p, value, t)
}

// Adds a member to the array on the Shard
func AddMember(c context.Context, name, tag, storeId, geo string, p Period, value string, t time.Time) error {
	AddMemberTask.Call(c, name, tag, storeId, geo, p, value, t)
	return nil
}

// Increment increments the named counter by 1
func Increment(c context.Context, name, tag, storeId, geo string, p Period, t time.Time) error {
	return IncrementBy(c, name, tag, storeId, geo, p, 1, t)
}

// Increment increments the named counter by amount
func IncrementBy(c context.Context, name, tag, storeId, geo string, p Period, amount int, t time.Time) error {
	IncrementByTask.Call(c, name, tag, storeId, geo, p, amount, t)
	return nil
}

// IncreaseShards increases the number of Shards for the named counter to n.
// It will never decrease the number of Shards.
func IncreaseShards(c context.Context, name string, n int) error {
	db := datastore.New(c)
	ckey := key.NewKey(c, ConfigKind, name, 0, nil)
	return datastore.RunInTransaction(c, func(txDb *datastore.Datastore) error {
		var cfg counterConfig
		mod := false
		err := db.Get(ckey, &cfg)
		if err == datastore.ErrNoSuchEntity {
			cfg.Shards = DefaultShards
			mod = true
		} else if err != nil {
			return err
		}
		if cfg.Shards < n {
			cfg.Shards = n
			mod = true
		}
		if mod {
			_, err = db.Put(ckey, &cfg)
		}
		return err
	}, nil)
}

var IncrementByTask *delay.Function
var AddMemberTask *delay.Function

func init() {
	IncrementByTask = delay.Func("IncrementByTask", func(c context.Context, name, tag, storeId, geo string, p Period, amount int, t time.Time) {
		log.Debug("INCREMENT %s BY %d", name, amount, c)
		db := datastore.New(c)

		// Get counter config.
		var cfg counterConfig
		ckey := key.NewKey(c, ConfigKind, name, 0, nil)
		err := datastore.RunInTransaction(c, func(txDb *datastore.Datastore) error {
			err := db.Get(ckey, &cfg)
			if err == datastore.ErrNoSuchEntity {
				cfg.Shards = DefaultShards
				_, err = db.Put(ckey, &cfg)
			}
			return err
		}, nil)
		err = datastore.IgnoreFieldMismatch(err)
		if err != nil {
			log.Panic("IncrementByTask Error %v", err, c)
		}
		var s Shard
		err = datastore.RunInTransaction(c, func(txDb *datastore.Datastore) error {
			ShardName := fmt.Sprintf("%s-Shard%d", name, rand.Intn(cfg.Shards))
			shardKey := key.NewKey(c, ShardKind, ShardName, 0, nil)
			err := db.Get(shardKey, &s)
			// A missing entity and a present entity will both work.
			err = datastore.IgnoreFieldMismatch(err)
			if err != nil && err != datastore.ErrNoSuchEntity {
				panic(err)
			}
			s.Name = name
			s.Tag = tag
			s.StoreId = storeId
			s.Period = p
			s.Geo = geo
			s.Count += amount
			s.Time = t
			_, err = db.Put(shardKey, &s)
			return err
		}, nil)
		if err == datastore.ErrConcurrentTransaction {
			IncreaseShards(c, name, 1)
			// Retry with delay using background goroutine
			go func() {
				time.Sleep(time.Duration(rand.Intn(30)) * time.Millisecond)
				IncrementByTask.Call(c, name, tag, storeId, geo, p, amount, t)
			}()
			return
		}
		err = datastore.IgnoreFieldMismatch(err)
		if err != nil {
			log.Panic("IncrementByTask Error %v", err, c)
		}
		cache.IncrementExisting(c, memcacheKey(name), int64(amount))
	})

	AddMemberTask = delay.Func("AddMember", func(c context.Context, name, tag, storeId, geo string, p Period, value string, t time.Time) {
		log.Debug("ADD MEMBER", c)
		db := datastore.New(c)

		// Get counter config.
		var cfg counterConfig
		ckey := key.NewKey(c, ConfigKind, name, 0, nil)
		err := datastore.RunInTransaction(c, func(txDb *datastore.Datastore) error {
			err := db.Get(ckey, &cfg)
			if err == datastore.ErrNoSuchEntity {
				cfg.Shards = DefaultShards
				_, err = db.Put(ckey, &cfg)
			}
			return err
		}, nil)
		err = datastore.IgnoreFieldMismatch(err)
		if err != nil {
			log.Panic("AddMemberTask Error %v", err, c)
		}
		var s Shard
		err = datastore.RunInTransaction(c, func(txDb *datastore.Datastore) error {
			ShardName := fmt.Sprintf("%s-Shard%d", name, rand.Intn(cfg.Shards))
			shardKey := key.NewKey(c, ShardKind, ShardName, 0, nil)
			err := db.Get(shardKey, &s)
			// A missing entity and a present entity will both work.
			err = datastore.IgnoreFieldMismatch(err)
			if err != nil && err != datastore.ErrNoSuchEntity {
				return err
			}
			s.Name = name
			if s.Set == nil {
				s.Set = make(map[string]bool)
			}
			s.Set[value] = true
			s.StoreId = storeId
			s.Period = p
			s.Geo = geo
			s.Time = t
			_, err = db.Put(shardKey, &s)
			return err
		}, nil)
		if err == datastore.ErrConcurrentTransaction {
			IncreaseShards(c, name, 1)
			// Retry with delay using background goroutine
			go func() {
				time.Sleep(time.Duration(rand.Intn(30)) * time.Millisecond)
				AddMemberTask.Call(c, name, tag, storeId, geo, p, value, t)
			}()
			return
		}
		err = datastore.IgnoreFieldMismatch(err)
		if err != nil {
			log.Panic("AddMemberTask Error %v", err, c)
		}

		mkey := memcacheKey(name)
		var members map[string]bool
		if _, err := cache.JSON.Get(c, mkey, &members); err == nil {
			members[value] = true
			cache.JSON.Set(c, &cache.Item{
				Key:        mkey,
				Object:     &members,
				Expiration: 60 * time.Second,
			})
		}
	})
}
