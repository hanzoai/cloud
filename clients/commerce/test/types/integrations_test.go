package test

import (
	"regexp"

	"github.com/hanzoai/cloud/clients/commerce/types/integration"
	// "github.com/hanzoai/cloud/clients/commerce/log"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("models/types/integrations", func() {
	Context("Append", func() {
		It("should return integrations with new one appended", func() {
			ins := integration.Integrations{}
			Expect(len(ins)).To(Equal(0))
			ins = ins.MustAppend(&integration.Integration{
				Type: integration.AnalyticsCustomType,
			})
			Expect(len(ins)).To(Equal(1))

			in := ins[0]

			Expect(in.CreatedAt.IsZero()).To(BeFalse())
			Expect(in.UpdatedAt.IsZero()).To(BeFalse())
		})

		It("should be immutable", func() {
			ins := integration.Integrations{}
			Expect(len(ins)).To(Equal(0))
			ins2 := ins.MustAppend(&integration.Integration{
				Type: integration.AnalyticsCustomType,
			})
			Expect(len(ins)).To(Equal(0))
			Expect(len(ins2)).To(Equal(1))

			in := ins2[0]

			Expect(in.CreatedAt.IsZero()).To(BeFalse())
			Expect(in.UpdatedAt.IsZero()).To(BeFalse())
		})

		It("should pass by reference", func() {
			ins := integration.Integrations{}
			Expect(len(ins)).To(Equal(0))
			in := &integration.Integration{
				Type: integration.AnalyticsCustomType,
			}
			ins = ins.MustAppend(in)

			// log.Warn(ins[0].Id)

			Expect(in.Id).ToNot(Equal(""))
			Expect(in.CreatedAt.IsZero()).To(BeFalse())
			Expect(in.UpdatedAt.IsZero()).To(BeFalse())
		})
	})

	Context("Update", func() {
		It("should return integrations with one updated", func() {
			ins := integration.Integrations{}
			Expect(len(ins)).To(Equal(0))

			ins = ins.MustAppend(&integration.Integration{
				Type: integration.AnalyticsCustomType,
			}).MustAppend(&integration.Integration{
				Type: integration.AnalyticsFacebookPixelType,
			}).MustAppend(&integration.Integration{
				Type: integration.AnalyticsCustomType,
			})

			Expect(len(ins)).To(Equal(3))

			in := integration.Integration{}
			in.Id = ins[0].Id
			in.Type = integration.MailchimpType
			in.Data = []byte("{ \"listId\": \"LIST\", \"apiKey\": \"APIKEY\" }")

			Expect(ins[0].Type).To(Equal(integration.AnalyticsCustomType))

			ins = ins.MustUpdate(&in)

			Expect(ins[0].Type).To(Equal(integration.MailchimpType))
			Expect(ins[0].UpdatedAt).To(Equal(in.UpdatedAt))
			Expect(ins[0].Mailchimp.ListId).To(Equal("LIST"))
			Expect(ins[0].Mailchimp.APIKey).To(Equal("APIKEY"))
		})

		It("should delegate to append", func() {
			ins := integration.Integrations{}
			Expect(len(ins)).To(Equal(0))
			ins = ins.MustUpdate(&integration.Integration{
				Type: integration.AnalyticsCustomType,
			})
			Expect(len(ins)).To(Equal(1))

			in := ins[0]

			Expect(in.CreatedAt.IsZero()).To(BeFalse())
			Expect(in.UpdatedAt.IsZero()).To(BeFalse())
		})

		It("should be immutable", func() {
			ins := integration.Integrations{}
			Expect(len(ins)).To(Equal(0))

			ins = ins.MustAppend(&integration.Integration{
				Type: integration.AnalyticsCustomType,
			}).MustAppend(&integration.Integration{
				Type: integration.AnalyticsFacebookPixelType,
			}).MustAppend(&integration.Integration{
				Type: integration.AnalyticsCustomType,
			})

			Expect(len(ins)).To(Equal(3))

			in := integration.Integration{}
			in.Id = ins[0].Id
			in.Type = integration.MailchimpType
			in.Data = []byte("{ \"listId\": \"LIST\", \"apiKey\": \"APIKEY\" }")

			Expect(ins[0].Type).To(Equal(integration.AnalyticsCustomType))

			ins2 := ins.MustUpdate(&in)

			Expect(ins[0].Type).To(Equal(integration.AnalyticsCustomType))
			Expect(ins[0].UpdatedAt).ToNot(Equal(ins[2].UpdatedAt))
			Expect(ins[0].Mailchimp.ListId).To(Equal(""))
			Expect(ins[0].Mailchimp.APIKey).To(Equal(""))

			Expect(ins2[0].Type).To(Equal(integration.MailchimpType))
			Expect(ins2[0].UpdatedAt).To(Equal(in.UpdatedAt))
			Expect(ins2[0].Mailchimp.ListId).To(Equal("LIST"))
			Expect(ins2[0].Mailchimp.APIKey).To(Equal("APIKEY"))
		})

		It("should update explicitly without data too", func() {
			ins := integration.Integrations{}
			Expect(len(ins)).To(Equal(0))

			ins = ins.MustAppend(&integration.Integration{
				Type: integration.AnalyticsCustomType,
			}).MustAppend(&integration.Integration{
				Type: integration.AnalyticsFacebookPixelType,
			}).MustAppend(&integration.Integration{
				Type: integration.AnalyticsCustomType,
			})

			Expect(len(ins)).To(Equal(3))

			in := integration.Integration{}
			in.Id = ins[0].Id
			in.Type = integration.MailchimpType
			in.Mailchimp.ListId = "LIST"
			in.Mailchimp.APIKey = "APIKEY"

			Expect(ins[0].Type).To(Equal(integration.AnalyticsCustomType))

			ins2 := ins.MustUpdate(&in)

			Expect(ins[0].Type).To(Equal(integration.AnalyticsCustomType))
			Expect(ins[0].UpdatedAt).ToNot(Equal(ins[2].UpdatedAt))
			Expect(ins[0].Mailchimp.ListId).To(Equal(""))
			Expect(ins[0].Mailchimp.APIKey).To(Equal(""))

			Expect(ins2[0].Type).To(Equal(integration.MailchimpType))
			Expect(ins2[0].UpdatedAt).To(Equal(in.UpdatedAt))
			Expect(ins2[0].Mailchimp.ListId).To(Equal("LIST"))
			Expect(ins2[0].Mailchimp.APIKey).To(Equal("APIKEY"))
		})
		It("should overwrite explicit with data", func() {
			ins := integration.Integrations{}
			Expect(len(ins)).To(Equal(0))

			ins = ins.MustAppend(&integration.Integration{
				Type: integration.AnalyticsCustomType,
			}).MustAppend(&integration.Integration{
				Type: integration.AnalyticsFacebookPixelType,
			}).MustAppend(&integration.Integration{
				Type: integration.AnalyticsCustomType,
			})

			Expect(len(ins)).To(Equal(3))

			in := integration.Integration{}
			in.Id = ins[0].Id
			in.Type = integration.MailchimpType
			in.Mailchimp.ListId = "L"
			in.Mailchimp.APIKey = "APIKEY"
			in.Data = []byte("{ \"listId\": \"LIST\" }")

			Expect(ins[0].Type).To(Equal(integration.AnalyticsCustomType))

			ins2 := ins.MustUpdate(&in)

			Expect(ins[0].Type).To(Equal(integration.AnalyticsCustomType))
			Expect(ins[0].UpdatedAt).ToNot(Equal(ins[2].UpdatedAt))
			Expect(ins[0].Mailchimp.ListId).To(Equal(""))
			Expect(ins[0].Mailchimp.APIKey).To(Equal(""))

			Expect(ins2[0].Type).To(Equal(integration.MailchimpType))
			Expect(ins2[0].UpdatedAt).To(Equal(in.UpdatedAt))
			Expect(ins2[0].Mailchimp.ListId).To(Equal("LIST"))
			Expect(ins2[0].Mailchimp.APIKey).To(Equal("APIKEY"))
		})
		It("should pass by reference", func() {
			ins := integration.Integrations{}
			Expect(len(ins)).To(Equal(0))

			in := &integration.Integration{
				Type: integration.AnalyticsCustomType,
			}

			ins = ins.MustAppend(in)

			Expect(in.Id).ToNot(Equal(""))
			Expect(in.CreatedAt.IsZero()).To(BeFalse())
			Expect(in.UpdatedAt.IsZero()).To(BeFalse())

			id := in.Id
			uat := in.UpdatedAt

			ins = ins.MustUpdate(in)

			Expect(id).To(Equal(in.Id))
			Expect(uat).ToNot(Equal(in.UpdatedAt))
		})
	})

	Context("Remove", func() {
		It("should return return integrations with one removed", func() {
			ins := integration.Integrations{}
			Expect(len(ins)).To(Equal(0))
			ins = ins.MustAppend(&integration.Integration{
				Type: integration.AnalyticsCustomType,
			}).MustAppend(&integration.Integration{
				Type: integration.AnalyticsFacebookPixelType,
			}).MustAppend(&integration.Integration{
				Type: integration.AnalyticsCustomType,
			})

			Expect(len(ins)).To(Equal(3))
			Expect(ins[0].Type).To(Equal(integration.AnalyticsCustomType))
			Expect(ins[1].Type).To(Equal(integration.AnalyticsFacebookPixelType))
			Expect(ins[2].Type).To(Equal(integration.AnalyticsCustomType))

			ins = ins.MustRemove(ins[1].Id)

			Expect(len(ins)).To(Equal(2))
			Expect(ins[0].Type).To(Equal(integration.AnalyticsCustomType))
			Expect(ins[1].Type).To(Equal(integration.AnalyticsCustomType))
		})

		It("should be immutable", func() {
			ins := integration.Integrations{}
			Expect(len(ins)).To(Equal(0))
			ins = ins.MustAppend(&integration.Integration{
				Type: integration.AnalyticsCustomType,
			}).MustAppend(&integration.Integration{
				Type: integration.AnalyticsFacebookPixelType,
			}).MustAppend(&integration.Integration{
				Type: integration.AnalyticsCustomType,
			})

			Expect(len(ins)).To(Equal(3))
			Expect(ins[0].Type).To(Equal(integration.AnalyticsCustomType))
			Expect(ins[1].Type).To(Equal(integration.AnalyticsFacebookPixelType))
			Expect(ins[2].Type).To(Equal(integration.AnalyticsCustomType))

			// log.Warn("ListA")
			// for _, in := range ins {
			// 	log.Warn("%s", in.Type)
			// }

			ins2 := ins.MustRemove(ins[1].Id)
			// log.Warn("ListC")
			// for _, in := range ins {
			// 	log.Warn("%s", in.Type)
			// }
			// log.Warn("ins %s", ins)

			Expect(len(ins)).To(Equal(3))
			Expect(ins[0].Type).To(Equal(integration.AnalyticsCustomType))
			Expect(ins[1].Type).To(Equal(integration.AnalyticsFacebookPixelType))
			Expect(ins[2].Type).To(Equal(integration.AnalyticsCustomType))

			Expect(len(ins2)).To(Equal(2))
			Expect(ins2[0].Type).To(Equal(integration.AnalyticsCustomType))
			Expect(ins2[1].Type).To(Equal(integration.AnalyticsCustomType))
		})
	})

	Context("FindById", func() {
		It("should find stuff that's there", func() {
			ins := integration.Integrations{}
			Expect(len(ins)).To(Equal(0))

			ins = ins.MustAppend(&integration.Integration{
				Type: integration.AnalyticsCustomType,
			}).MustAppend(&integration.Integration{
				Type: integration.AnalyticsFacebookPixelType,
			}).MustAppend(&integration.Integration{
				Type: integration.AnalyticsCustomType,
			})

			Expect(len(ins)).To(Equal(3))

			in, err := ins.FindById(ins[1].Id)
			Expect(err).ToNot(HaveOccurred())
			Expect(in.Id).To(Equal(ins[1].Id))
		})

		It("should not find stuff that's not there", func() {
			ins := integration.Integrations{}
			Expect(len(ins)).To(Equal(0))

			ins = ins.MustAppend(&integration.Integration{
				Type: integration.AnalyticsCustomType,
			}).MustAppend(&integration.Integration{
				Type: integration.AnalyticsFacebookPixelType,
			}).MustAppend(&integration.Integration{
				Type: integration.AnalyticsCustomType,
			})

			Expect(len(ins)).To(Equal(3))

			in, err := ins.FindById("NOT HERE")
			Expect(err).To(HaveOccurred())
			Expect(in).To(BeNil())
		})
	})

	Context("FilterByType", func() {
		It("should find stuff", func() {
			ins := integration.Integrations{}
			Expect(len(ins)).To(Equal(0))

			ins = ins.MustAppend(&integration.Integration{
				Type: integration.AnalyticsCustomType,
			}).MustAppend(&integration.Integration{
				Type: integration.AnalyticsFacebookPixelType,
			}).MustAppend(&integration.Integration{
				Type: integration.AnalyticsCustomType,
			})

			Expect(len(ins)).To(Equal(3))

			results := ins.FilterByType(integration.AnalyticsCustomType)
			Expect(len(results)).To(Equal(2))
		})
	})

	Context("integration.Encode/integration.Decode", func() {
		It("should encode/decode stuff", func() {
			in := integration.Integration{}
			in.Type = integration.MailchimpType
			in.Data = []byte("{ \"listId\": \"LIST\", \"apiKey\": \"APIKEY\" }")

			inD := integration.Integration{}

			err := integration.Decode(&in, &inD)
			Expect(err).ToNot(HaveOccurred())
			Expect(inD.Type).To(Equal(integration.MailchimpType))
			Expect(inD.Mailchimp.ListId).To(Equal("LIST"))
			Expect(inD.Mailchimp.APIKey).To(Equal("APIKEY"))

			inE := integration.Integration{}

			r, _ := regexp.Compile("\\s")

			err = integration.Encode(&inD, &inE)
			Expect(err).ToNot(HaveOccurred())
			Expect(inE.Type).To(Equal(integration.MailchimpType))
			Expect(r.ReplaceAllString(string(inE.Data), "")).To(Equal("{\"listId\":\"LIST\",\"apiKey\":\"APIKEY\"}"))
		})
	})
})
