package fake

import (
	"math/rand"

	"github.com/icrowley/fake"
)

// Aliases to fake for convenience
var Brand = fake.Brand
var Character = fake.Character
var Characters = fake.CharactersN
var City = fake.City
var Color = fake.Color
var Comment = fake.EmailBody
var Company = fake.Company
var Continent = fake.Continent
var CreditCardNum = fake.CreditCardNum
var CreditCardType = fake.CreditCardType
var Day = fake.Day
var Digits = fake.DigitsN
var DomainName = fake.DomainName
var DomainZone = fake.DomainZone
var EmailAddress = fake.EmailAddress
var EmailBody = fake.EmailBody
var EmailSubject = fake.EmailSubject
var FirstName = fake.FirstName
var FullName = fake.FullName
var FullNameWithPrefix = fake.FullNameWithPrefix
var FullNameWithSuffix = fake.FullNameWithSuffix
var Gender = fake.Gender
var HexColor = fake.HexColor
var HexColorShort = fake.HexColorShort
var IPv4 = fake.IPv4
var Industry = fake.Industry
var JobTitle = fake.JobTitle
var Language = fake.Language
var LastName = fake.LastName
var Latitude = fake.Latitude
var Longitude = fake.Longitude
var Model = fake.Model
var Month = fake.Month
var MonthNum = fake.MonthNum
var MonthShort = fake.MonthShort
var Number = rand.Intn
var Paragraph = fake.Paragraph
var Paragraphs = fake.ParagraphsN
var Password = fake.Password
var Percent = rand.Float64()
var Phone = fake.Phone
var ProductName = fake.ProductName
var Rating = rand.Intn
var Sentence = fake.Sentence
var Sentences = fake.SentencesN
var SimplePassword = fake.SimplePassword
var State = fake.State
var StateAbbrev = fake.StateAbbrev
var Street = fake.Street
var StreetAddress = fake.StreetAddress
var Title = fake.Title
var TopLevelDomain = fake.TopLevelDomain
var Username = fake.UserName
var WeekDay = fake.WeekDay
var WeekDayShort = fake.WeekDayShort
var WeekdayNum = fake.WeekdayNum
var Word = fake.Word
var Words = fake.WordsN
var Year = fake.Year
var Zip = fake.Zip
