package recaptcha

import (
	"context"
	"io/ioutil"
	"net/http"
	"net/url"
	"time"

	"github.com/hanzoai/cloud/clients/commerce/log"
	"github.com/hanzoai/cloud/clients/commerce/util/json"
)

type RecaptchaResponse struct {
	Success     bool      `json:"success"`
	ChallengeTS time.Time `json:"challenge_ts"`
	Hostname    string    `json:"hostname"`
	ErrorCodes  []string  `json:"error-codes"`
}

func Challenge(ctx context.Context, privateKey, response string) bool {
	// log.Warn("Captcha:\n\n%s\n\n%s\n\n%s", privateKey, response, ctx)
	client := &http.Client{Timeout: 55 * time.Second}
	r := RecaptchaResponse{}
	resp, err := client.PostForm("https://www.google.com/recaptcha/api/siteverify",
		url.Values{
			"secret":   {privateKey},
			"response": {response},
			// "remoteip": {remoteIp},
		})
	if err != nil {
		log.Error("Captcha post error: %s", err, ctx)
	}
	defer resp.Body.Close()
	body, err := ioutil.ReadAll(resp.Body)
	log.Info("Captcha %s", body, ctx)
	if err != nil {
		log.Error("Read error: could not read body: %s", err, ctx)
		return false
	}
	err = json.Unmarshal(body, &r)
	log.Info("Captcha %v", json.Encode(r), ctx)
	if err != nil {
		log.Error("Read error: got invalid JSON: %s", err, ctx)
		return false
	}

	return r.Success
}
