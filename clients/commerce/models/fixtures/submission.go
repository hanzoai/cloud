package fixtures

import (
	"github.com/gin-gonic/gin"

	"github.com/hanzoai/cloud/clients/commerce/models/submission"
)

var Submission = New("submission", func(c *gin.Context) *submission.Submission {
	db := getNamespaceDb(c)

	sub := submission.New(db)
	sub.Email = "fan@suchfan.com"
	sub.Metadata["message"] = "Hi I am a fan!"

	sub.MustPut()

	return sub
})
