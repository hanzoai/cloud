package form

import (
	"time"

	"github.com/gin-gonic/gin"

	"github.com/hanzoai/cloud/clients/commerce/config"
	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/email"
	"github.com/hanzoai/cloud/clients/commerce/log"
	"github.com/hanzoai/cloud/clients/commerce/models/form"
	"github.com/hanzoai/cloud/clients/commerce/models/organization"
	"github.com/hanzoai/cloud/clients/commerce/models/subscriber"
	"github.com/hanzoai/cloud/clients/commerce/models/types/client"
	"github.com/hanzoai/cloud/clients/commerce/thirdparty/mailchimp"
	"github.com/hanzoai/cloud/clients/commerce/util/counter"
	"github.com/hanzoai/cloud/clients/commerce/util/json"
	"github.com/hanzoai/cloud/clients/commerce/util/json/http"
)

var subscriberEndpoint = config.UrlFor("api", "/subscriber/")

func subscribe(c *gin.Context, db *datastore.Datastore, org *organization.Organization, f *form.Form) {
	ctx := db.Context

	// Make sure Subscriber is created with the right context
	s := subscriber.New(db)

	// Decode response body for subscriber
	if err := json.Decode(c.Request.Body, s); err != nil {
		http.Fail(c, 400, "Failed decode request body", err)
		return
	}

	// Store metadata about client
	s.Client = client.New(c)

	// Save subscriber to mailing list
	if err := f.AddSubscriber(s); err != nil {
		if err == form.SubscriberAlreadyExists {
			http.Fail(c, 409, "Subscriber already exists", nil)
			return
		}
		http.Fail(c, 500, "Failed to save subscriber to mailing list", err)
		return
	}

	// Increment subscribers
	counter.IncrSubscriber(ctx, time.Now())

	if err := counter.IncrSubscribers(ctx, org, f.Id(), time.Now()); err != nil {
		log.Error("IncrSubscriber Error: %v", err, c)
	}

	if f.EmailList.Enabled {
		email.Subscribe(ctx, f, s, org)
	}

	// Send welcome email
	if f.SendWelcome {
		email.SendSubscriberWelcome(ctx, org, s, f)
	}

	// Save user as customer in Mailchimp if configured
	if f.Mailchimp.APIKey != "" {
		// Create new mailchimp client
		client := mailchimp.New(ctx, org.Mailchimp)

		log.Info("Mailchimp Subscribe Metadata %v", s.Metadata, c)

		mf := s.Metadata["firstName"]
		firstName := ""
		if mf != nil {
			firstName = mf.(string)
		}
		if firstName == "" {
			mf = s.Metadata["FNAME"]
			if mf != nil {
				firstName = mf.(string)
			}
		}

		ml := s.Metadata["lastName"]

		lastName := ""
		if ml != nil {
			lastName = ml.(string)
		}
		if lastName == "" {
			ml = s.Metadata["LNAME"]
			if ml != nil {
				lastName = ml.(string)
			}
		}

		if err := client.SubscribeForm(f.Mailchimp.ListId, s.Email, firstName, lastName); err != nil {
			log.Error("Mailchimp Subscribe Error: %v", err, c)
		}
	}

	// Forward subscriber (if enabled)
	if f.Forward.Enabled {
		forward(ctx, org, f, s)
	}

	// Success!
	c.Writer.Header().Add("Location", subscriberEndpoint+s.Id())
	http.Render(c, 201, s)
}
