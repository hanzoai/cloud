package integration

import (
	"errors"
	"fmt"
	"reflect"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/log"
	"github.com/hanzoai/cloud/clients/commerce/models/mixin"
	"github.com/hanzoai/cloud/clients/commerce/test-integration/datastore/tasks"
	"github.com/hanzoai/cloud/clients/commerce/util/gincontext"
	"github.com/hanzoai/cloud/clients/commerce/util/test/ae"

	. "github.com/hanzoai/cloud/clients/commerce/util/test/ginkgo"
)

func Test(t *testing.T) {
	Setup("datastore/integration", t)
}

var (
	c   *gin.Context
	ctx ae.Context
	db  *datastore.Datastore
)

var _ = BeforeSuite(func() {
	ctx = ae.NewContext()
	c = gincontext.New(ctx)
	db = datastore.New(ctx)
})

var _ = AfterSuite(func() {
	ctx.Close()
})

func checkCountValue(entity mixin.Entity, numModels int, expected int) {
	err := Retry(3, func() error {
		models := entity.Slice()
		_, err := entity.Query().All().GetAll(models)
		if err != nil {
			log.Error("Failed to get models from datastore: %v", err)
			return err
		}

		slice := reflect.Indirect(reflect.ValueOf(models))

		Expect(slice.Len()).To(Equal(numModels))

		// Make sure expected count is right
		for i := 0; i < slice.Len(); i++ {
			model := slice.Index(i)
			count := 0
			switch v := model.Interface().(type) {
			case *tasks.Model:
				count = v.Count
			case *tasks.Model2:
				count = v.Count
			}
			if count != expected {
				return errors.New(fmt.Sprintf("Task did not set value on model correctly, expected: %v, found: %v, models: %#v", expected, count, models))
			}
		}

		return nil
	})
	Expect(err).NotTo(HaveOccurred())
}

var _ = Describe("datastore/parallel", func() {
	Context("With task", func() {
		It("Should run tasks in parallel", func() {
			// Prepoulate database with 10 entities
			for i := 0; i < 10; i++ {
				model := tasks.NewModel(db)
				err := model.Put()
				Expect(err).NotTo(HaveOccurred())
			}

			// Run task in parallel
			tasks.TaskPlus1.Run(c, 2)

			// Check if our entities have been updated
			checkCountValue(tasks.NewModel(db), 10, 1)
		})
	})

	Context("With task taking optional argument", func() {
		It("Should run tasks in parallel", func() {
			// Prepoulate database with 10 entities
			for i := 0; i < 10; i++ {
				model := tasks.NewModel2(db)
				err := model.Put()
				Expect(err).NotTo(HaveOccurred())
			}

			// Run task in parallel
			tasks.TaskSetVal.Run(c, 2, 100)

			checkCountValue(tasks.NewModel2(db), 10, 100)
		})
	})
})
