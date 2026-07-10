package account

import (
	"errors"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/email"
	"github.com/hanzoai/cloud/clients/commerce/log"
	"github.com/hanzoai/cloud/clients/commerce/middleware"
	"github.com/hanzoai/cloud/clients/commerce/models/token"
	"github.com/hanzoai/cloud/clients/commerce/models/user"
	"github.com/hanzoai/cloud/clients/commerce/util/json"
	"github.com/hanzoai/cloud/clients/commerce/util/json/http"
)

type twoStageEnableReq struct {
	*user.User

	Password        string `json:"password"`
	PasswordConfirm string `json:"passwordConfirm"`
}

func (r twoStageEnableReq) GetPassword() string {
	return r.Password
}

func (r twoStageEnableReq) GetPasswordConfirm() string {
	return r.PasswordConfirm
}

func enable(c *gin.Context) {
	org := middleware.GetOrganization(c)
	db := datastore.New(org.Namespaced(c))

	usr := user.New(db)
	tok := token.New(db)

	// Get Token
	id := c.Params.ByName("tokenid")
	if err := tok.GetById(id); err != nil {
		panic(err)
	}

	// Get user associated with token
	if err := usr.GetById(tok.UserId); err != nil {
		panic(err)
	}

	if tok.Expired() || tok.Used {
		http.Fail(c, 403, "Token expired", errors.New("token expired"))
		return
	}

	if org.SignUpOptions.TwoStageEnabled {
		usr.Email = strings.ToLower(strings.TrimSpace(usr.Email))

		req := &twoStageEnableReq{User: usr}

		if err := json.Decode(c.Request.Body, req); err != nil {
			http.Fail(c, 400, "Failed decode request body", err)
			return
		}

		if req.Password != "" {
			if err := resetPassword(usr, req); err != nil {
				switch err {
				case ErrPasswordMismatch, ErrPasswordMinLength:
					http.Fail(c, 400, err.Error(), err)
					return
				}
			}
		}
	}

	// Set user as enabled
	usr.Enabled = true
	if err := usr.Put(); err != nil {
		http.Fail(c, 500, "Failed to enable user", err)
		return
	}

	// Token reuseable if no password is set
	if len(usr.PasswordHash) > 0 {
		// Save token
		tok.Used = true
		if err := tok.Put(); err != nil {
			log.Warn("Unable to update token", err, c)
		}
	}

	// Send account confirmed email
	ctx := middleware.GetContext(c)
	email.SendUserActivated(ctx, org, usr)

	loginTok := middleware.GetToken(c)
	loginTok.UserId = usr.Id()
	loginTok.ExpirationTime = time.Now().Add(time.Hour * 24 * 7).Unix()

	http.Render(c, 200, gin.H{"status": "ok", "token": loginTok.Encode(org.SecretKey)})
}
