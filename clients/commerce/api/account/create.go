package account

import (
	"errors"
	"regexp"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/hanzoai/cloud/clients/commerce/auth/password"
	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/email"
	"github.com/hanzoai/cloud/clients/commerce/log"
	"github.com/hanzoai/cloud/clients/commerce/middleware"
	"github.com/hanzoai/cloud/clients/commerce/models/form"
	"github.com/hanzoai/cloud/clients/commerce/models/referral"
	"github.com/hanzoai/cloud/clients/commerce/models/referrer"
	"github.com/hanzoai/cloud/clients/commerce/models/subscriber"
	"github.com/hanzoai/cloud/clients/commerce/models/types/currency"
	"github.com/hanzoai/cloud/clients/commerce/models/user"
	"github.com/hanzoai/cloud/clients/commerce/thirdparty/mailchimp"
	"github.com/hanzoai/cloud/clients/commerce/thirdparty/recaptcha"
	"github.com/hanzoai/cloud/clients/commerce/util/counter"
	"github.com/hanzoai/cloud/clients/commerce/util/json"
	"github.com/hanzoai/cloud/clients/commerce/util/json/http"
)

var emailRegex = regexp.MustCompile(`(\w[-._\w]*@\w[-._\w]*\w\.\w{2,20})`)
var usernameRegex = regexp.MustCompile(`^[a-z0-9_\-\.]+$`)

type createReq struct {
	*user.User
	Email           string `json:"email"`
	Password        string `json:"password"`
	PasswordConfirm string `json:"passwordConfirm"`
	Captcha         string `json:"g-recaptcha-response"`
	StoreId         string `json:"storeId"`
}

type createRes struct {
	*user.User
	Token string `json:"token,omitempty"`
}

type Referrent struct {
	id   string
	kind string
}

func (r *Referrent) Id() string {
	return r.id
}

func (r *Referrent) Kind() string {
	return r.kind
}

func (r *Referrent) Total() currency.Cents {
	return currency.Cents(1)
}

func create(c *gin.Context) {
	org := middleware.GetOrganization(c)
	db := datastore.New(org.Namespaced(c))

	req := &createReq{}
	req.User = user.New(db)

	// Default these fields to exotic unicode character to test if they are set to empty
	req.Username = "\u263A"
	req.Email = "\u263A"
	req.FirstName = "\u263A"
	req.LastName = "\u263A"
	req.FormId = "\u263A"

	log.Info("Decoding User Creation Request", c)
	// Decode response body to create new user
	if err := json.Decode(c.Request.Body, req); err != nil {
		http.Fail(c, 400, "Failed decode request body", err)
		return
	}

	// Pull out user
	usr := req.User
	usr.Email = req.Email

	log.Info("Fetching User Request: %v", json.Encode(usr), c)
	// Email is required
	if usr.Email == "" || usr.Email == "\u263A" {
		http.Fail(c, 400, "Email is required", errors.New("email is required"))
		return
	}

	// If the username is purposely blank or username is required by the
	// organization...
	if usr.Username == "" || (org.SignUpOptions.UsernameRequired && usr.Username == "\u263A") {
		http.Fail(c, 400, "Username is required", errors.New("username is required"))
		return
	}

	usr.Email = strings.ToLower(strings.TrimSpace(usr.Email))
	usr.Username = strings.ToLower(strings.TrimSpace(usr.Username))
	un := usr.Username
	uf := usr.FormId

	usr2 := user.New(db)
	// Email can't already exist or if it does, can't have a password
	if err := usr2.GetByEmail(usr.Email); err == nil {
		if len(usr2.PasswordHash) > 0 {
			http.Fail(c, 400, "Email is in use", errors.New("email is in use"))
			return
		} else {
			// Transfer name from request user to queried out user if successful
			req.User = usr2
			if usr.FirstName != "" && usr.FirstName != "\u263A" {
				usr2.FirstName = usr.FirstName
			}
			if usr.LastName != "" && usr.FirstName != "\u263A" {
				usr2.LastName = usr.LastName
			}
			// Username isn't set in stone until actually registered
			if un != "" && un != "\u263A" {
				usr2.Username = un
			}
			// Username isn't set in stone until actually registered
			if uf != "" && uf != "\u263A" {
				usr2.FormId = uf
			}

			usr = usr2
		}
	}

	if !org.SignUpOptions.NoNameRequired {
		log.Info("Sign up does require Name: %s/%s", usr.FirstName, usr.LastName, c)
		if usr.FirstName == "" || usr.FirstName == "\u263A" {
			http.Fail(c, 400, "First name cannot be blank", errors.New("first name cannot be blank"))
			return
		}

		// if usr.LastName == "" || usr.LastName == "\u263A" {
		// 	http.Fail(c, 400, "Last name cannot be blank", errors.New("Last name cannot be blank"))
		// 	return
		// }
	} else {
		log.Info("Sign up does not require Name", c)
	}

	if org.SignUpOptions.AllowAffiliateSignup {
		log.Info("Signing up as Affiliate? %v", req.User.IsAffiliate, c)
		usr.IsAffiliate = req.User.IsAffiliate
	}

	if uf == "\u263A" {
		usr.FormId = ""
	}

	if un == "\u263A" {
		usr.Username = ""
	} else {
		usr3 := user.New(db)
		// Username can't exist on another user
		if err := usr3.GetByUsername(usr.Username); err == nil {
			if usr2.Id() != usr3.Id() {
				http.Fail(c, 400, "Username is in use", errors.New("Username is in use"))
				return
			}
		}

		// Username must be valid if it exists
		log.Info("Checking if Username is valid", c)
		if ok := usernameRegex.MatchString(usr.Username); !ok {
			http.Fail(c, 400, "Username '"+usr.Username+"' is not valid", errors.New("Username '"+usr.Username+"' is not valid"))
			return
		}
	}

	if usr.Email == "\u263A" {
		usr.Email = ""
	}

	if usr.FirstName == "\u263A" {
		usr.FirstName = ""
	}

	if usr.LastName == "\u263A" {
		usr.LastName = ""
	}

	// Email must be valid
	log.Info("Checking if User email is valid", c)
	if ok := emailRegex.MatchString(usr.Email); !ok {
		http.Fail(c, 400, "Email '"+usr.Email+"' is not valid", errors.New("email '"+usr.Email+"' is not valid"))
		return
	}

	if !org.SignUpOptions.NoPasswordRequired {
		log.Info("Sign up requires password", c)
		// Password should be at least 6 characters long
		if len(req.Password) < 6 {
			http.Fail(c, 400, "Password needs to be atleast 6 characters", errors.New("password needs to be atleast 6 characters"))
			return
		}

		// Password confirm must match
		if req.Password != req.PasswordConfirm { // pragma: allowlist secret
			http.Fail(c, 400, "Passwords need to match", errors.New("passwords need to match"))
			return
		}

		// Hash password
		if hash, err := password.Hash(req.Password); err != nil { // pragma: allowlist secret
			http.Fail(c, 400, "Failed to hash user password", err)
		} else {
			usr.PasswordHash = hash
		}
	} else {
		log.Info("Sign up does not require password", c)
	}

	if org.Recaptcha.Enabled && !recaptcha.Challenge(db.Context, org.Recaptcha.SecretKey, req.Captcha) {
		http.Fail(c, 400, "Captcha needs to be completed", errors.New("captcha needs to be completed"))
		return
	}

	ctx := org.Datastore().Context
	if err := counter.IncrUsers(ctx, org, time.Now()); err != nil {
		log.Warn("Redis Error %s", err, ctx)
	}

	enabled := org.SignUpOptions.AccountsEnabledByDefault || usr.PreApproved
	// Test key users are automatically confirmed
	if !org.Live {
		usr.Enabled = true
	} else {
		log.Info("User is enabled? %v", usr.Enabled, enabled, c)
		usr.Enabled = enabled
	}

	// Determine store to use
	storeId := req.StoreId
	if storeId == "" {
		storeId = org.DefaultStore
	}

	usr.StoreId = storeId

	// Save new user
	log.Info("User is attributed to store: %v", storeId, c)
	if err := usr.Put(); err != nil {
		http.Fail(c, 400, "Failed to create user", err)
	}

	ref := referrer.New(usr.Datastore())

	// if ReferrerId refers to non-existing token, then remove from order
	if usr.ReferrerId != "" {
		log.Info("User is attributed to Referrer %s", usr.ReferrerId, c)
		if err := ref.GetById(usr.ReferrerId); err != nil {
			usr.ReferrerId = ""
		} else {
			// Try to save referral, save updated referrer
			if _, err := ref.SaveReferral(org.Datastore().Context, org.Id(), referral.NewUser, &Referrent{
				usr.Id(),
				usr.Kind(),
			}, usr.Id(), !org.Live); err != nil {
				log.Warn("Unable to save referral: %v", err, c)
			}
		}
	}

	// Starter credit is granted by Commerce when a payment method is added
	// (see api/billing/payment_methods.go:grantStarterCreditIfEligible),
	// not at signup. This prevents abuse from mass-created accounts.

	tokStr := ""

	if org.SignUpOptions.ImmediateLogin || usr.PreApproved {
		log.Info("User is being immediately logged in", c)
		loginTok := middleware.GetToken(c)
		loginTok.UserId = usr.Id()
		loginTok.ExpirationTime = time.Now().Add(time.Hour * 24 * 7).Unix()
		tokStr = loginTok.Encode(org.SecretKey)
	}

	counter.IncrUser(usr.Context(), usr.CreatedAt)

	log.Info("Sending Emails", c)

	if usr.FormId != "" {
		log.Info("Adding User %v To Form %v", usr.Id(), usr.FormId, c)
		f := form.New(usr.Datastore())

		if err := f.GetById(usr.FormId); err != nil {
			log.Warn("Form %v does not exist.", usr.FormId, c)
		}

		s := subscriber.New(db)
		s.Email = usr.Email
		s.UserId = usr.Id()
		s.FirstName = usr.FirstName
		s.LastName = usr.LastName
		s.Tags = []string{"account-creation"}

		if err := f.AddSubscriber(s); err != nil {
			log.Info("Subscriber %v Encountered Non-Fatal Error: %v", usr.Id(), err, c)
		}

		if f.EmailList.Enabled {
			email.Subscribe(ctx, f, s, org)
		}
	}

	// Send welcome, email confirmation emails
	if !enabled {
		email.SendUserConfirmEmail(ctx, org, usr)
	}

	if usr.IsAffiliate && org.Email.Get(email.AffiliateWelcome).Enabled {
		email.SendAffiliateWelcome(ctx, org, usr)
	} else {
		email.SendUserWelcome(ctx, org, usr)
	}

	// Save user as customer in Mailchimp if configured
	if org.Mailchimp.APIKey != "" {
		log.Info("Saving User to Mailchimp: %v", json.Encode(usr), c)
		// Create new mailchimp client
		client := mailchimp.New(ctx, org.Mailchimp)

		// Create customer in mailchimp for this user
		if err := client.CreateCustomer(storeId, usr); err != nil {
			log.Warn("Failed to create Mailchimp customer: %v", err, ctx)
		}
	} else {
		log.Info("Skip saving User to Mailchimp: %v", json.Encode(usr), c)
	}

	http.Render(c, 201, createRes{User: usr, Token: tokStr})
}
