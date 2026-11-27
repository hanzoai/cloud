package graph

// ontology.go is the shipped vocabulary of Apple's Personal Knowledge Graph
// (~/Library/IntelligencePlatform/{graph.db,ontology.db,entityTagging.db} on
// macOS 13+ and /private/var/mobile/Library/IntelligencePlatform on iOS):
//
//   - 181 entity classes (class.json): SB*, CS* and nm_* across core, person,
//     attribute, place, event, activity, activity type, transport, media,
//     animal, commerce and the 46 edge relationship-type classes that reify
//     two-row edges sharing a relationshipId.
//   - 278 predicates (predicate.json): PS* and nm_* with what each records,
//     the canonical relation name this plane files it under, whether its
//     object names another entity, the OS data source and the inference method.
//   - 144 behavioural features (EntityTagging_Family.mlmodelc/metadata.json):
//     indices 0..19 identity, 20..59 communication, 60..143 photos.
//   - 17 kinship scorecard columns (entityTagging.db): per-person closeness
//     and family probabilities recomputed daily by knowledgeconstructiond.

import (
	"context"
	"strings"
	"unicode"
)

// graphOntologyIn filters the personal knowledge graph ontology catalog.
type graphOntologyIn struct {
	// Group narrows classes or features to one group, such as core, person,
	// activity, edge, identity, communication or photos. Absent returns every
	// group.
	Group string `query:"group" json:"group,omitempty"`
	// Q narrows classes, predicates and features to entries whose code, label,
	// canonical relation or source contains this substring, case-insensitively.
	// Absent returns the whole catalog.
	Q string `query:"q" json:"q,omitempty"`
}

// graphOntologyClass is one of the 181 entity classes declared in Apple's
// class.json.
type graphOntologyClass struct {
	// ID is the raw code stored in graph.db, such as SB104, CS165 or nm_walletOrder.
	ID string `json:"id"`
	// Label is the human-readable class name, such as person or employment
	// relationship type.
	Label string `json:"label"`
	// Group is the class family: core, person, attribute, place, event,
	// activity, activity type, transport, media, animal, commerce or edge.
	Group string `json:"group"`
	// Edge is true for the relationship-type classes (CS* and nm_*RelationshipType)
	// that reify a two-row edge sharing a relationshipId in stable_graph.
	Edge bool `json:"edge,omitempty"`
	// Mechanism records whether the daemon mechanism populating the class is
	// implemented, unverified or unlocated.
	Mechanism string `json:"mechanism"`
}

// graphOntologyPredicate is one of the 278 predicates declared in Apple's
// predicate.json.
type graphOntologyPredicate struct {
	// ID is the raw predicate code stored in graph.db, such as PS1, PS500 or
	// nm_isCurrentUser.
	ID string `json:"id"`
	// Label is the plain-language reading of what the predicate records.
	Label string `json:"label"`
	// Relation is the canonical snake_case relation key this plane files the
	// predicate under when ingesting an Apple graph.
	Relation string `json:"relation"`
	// Names is true when the predicate links its subject to another entity
	// rather than a scalar literal.
	Names bool `json:"names,omitempty"`
	// Source names the OS store or subsystem the daemon reads the predicate from.
	Source string `json:"source"`
	// Method names how the daemon obtains the value: Copied, Joined, Rule,
	// Apple-shipped, Embedding model, XGBoost, Face clustering or Local LLM.
	Method string `json:"method"`
	// Mechanism records whether the daemon mechanism is implemented, unverified
	// or unlocated.
	Mechanism string `json:"mechanism"`
}

// graphOntologyFeature is one of the 144 numeric inputs to the EntityTagging
// XGBoost classifiers.
type graphOntologyFeature struct {
	// Index is the feature's 0-based position (0..143) in metadata.json.
	Index int `json:"index"`
	// Name is Apple's feature identifier, such as sameLastName or
	// photosPersonPosteriorProbabilityGivenSaturdayNight.
	Name string `json:"name"`
	// Group is the feature block: identity (0..19), communication (20..59) or
	// photos (60..143).
	Group string `json:"group"`
}

// graphOntologyOut is the personal knowledge graph ontology catalog.
type graphOntologyOut struct {
	// Classes is the matching entity classes (181 in the unfiltered catalog).
	Classes []graphOntologyClass `json:"classes"`
	// Predicates is the matching predicates (278 in the unfiltered catalog).
	Predicates []graphOntologyPredicate `json:"predicates"`
	// Features is the matching EntityTagging classifier features (144 in the
	// unfiltered catalog).
	Features []graphOntologyFeature `json:"features"`
	// ScoreColumns is the 17 relationship columns of entityTagging.db's
	// kinship scorecard.
	ScoreColumns []string `json:"score_columns"`
}

// ontology lists the entity classes, predicates, classifier features and
// kinship scorecard columns of the personal knowledge graph ontology.
//
// Every code graph.db and entityTagging.db write on macOS and iOS is here with
// its plain-language reading and the canonical relation key graphApple maps it
// to. Filter by group or by substring q.
//
// Time: it takes no as_of or as_known; the shipped catalog is static.
//
// Example: {"group": "edge", "q": "employment"}
func (o ops) ontology(_ context.Context, in *graphOntologyIn) (*graphOntologyOut, error) {
	group := strings.ToLower(strings.TrimSpace(in.Group))
	q := strings.ToLower(strings.TrimSpace(in.Q))

	classes := make([]graphOntologyClass, 0, len(appleClasses))
	for _, c := range appleClasses {
		if group != "" && strings.ToLower(c.Group) != group {
			continue
		}
		if q != "" && !containsAny(q, c.ID, c.Label, c.Group) {
			continue
		}
		classes = append(classes, c)
	}

	preds := make([]graphOntologyPredicate, 0, len(applePredicates))
	for _, p := range applePredicates {
		if group != "" && !strings.Contains(strings.ToLower(p.Source), group) && !strings.Contains(strings.ToLower(p.Method), group) {
			continue
		}
		if q != "" && !containsAny(q, p.ID, p.Label, p.Relation, p.Source, p.Method) {
			continue
		}
		preds = append(preds, p)
	}

	feats := make([]graphOntologyFeature, 0, len(appleFeatures))
	for _, f := range appleFeatures {
		if group != "" && strings.ToLower(f.Group) != group {
			continue
		}
		if q != "" && !containsAny(q, f.Name, f.Group) {
			continue
		}
		feats = append(feats, f)
	}

	cols := make([]string, len(appleScoreColumns))
	copy(cols, appleScoreColumns)
	return &graphOntologyOut{
		Classes:      classes,
		Predicates:   preds,
		Features:     feats,
		ScoreColumns: cols,
	}, nil
}

func containsAny(needle string, haystacks ...string) bool {
	for _, h := range haystacks {
		if strings.Contains(strings.ToLower(h), needle) {
			return true
		}
	}
	return false
}

// appleScoreColumns is the 17 relationship columns in entityTagging.db's
// entity_tagging table.
var appleScoreColumns = []string{
	"family",
	"familyAndFriends",
	"parent",
	"mother",
	"father",
	"child",
	"son",
	"daughter",
	"sibling",
	"brother",
	"sister",
	"partner",
	"coworker",
	"friend",
	"alumni",
	"myself",
	"unknown",
}

// appleClassRaw is a compact row for building appleClasses.
type appleClassRaw struct{ id, label, group, mech string }

var appleClassesRaw = []appleClassRaw{
	// core (19)
	{"SB56", "entity", "core", "implemented"},
	{"SB104", "person", "core", "implemented"},
	{"SB152", "place", "core", "implemented"},
	{"nm_topic", "topic", "core", "implemented"},
	{"SB65", "event", "core", "implemented"},
	{"nm_document", "document", "core", "could not find mechanism"},
	{"SB473", "home", "core", "could not find mechanism"},
	{"SB10", "organization", "core", "implemented"},
	{"SB2", "group of humans", "core", "implemented"},
	{"SB144", "software", "core", "implemented"},
	{"SB193", "organism", "core", "implemented"},
	{"SB198", "agent", "core", "implemented"},
	{"SB83", "concept", "core", "implemented"},
	{"SB400", "occurrence", "core", "implemented"},
	{"SB60", "role", "core", "implemented"},
	{"SB197", "siribase type", "core", "implemented"},
	{"nm_knowledgeSource", "knowledge source", "core", "implemented"},
	{"SB170", "time", "core", "could not find mechanism"},
	{"SB175", "group", "core", "implemented"},
	// person (9)
	{"SB470", "device primary user", "person", "could not find mechanism"},
	{"SB199", "musical artist", "person", "could not find mechanism"},
	{"SB14", "sports team", "person", "implemented"},
	{"SB417", "sports organization", "person", "could not find mechanism"},
	{"SB115", "sports league", "person", "could not find mechanism"},
	{"SB185", "athlete", "person", "could not find mechanism"},
	{"SB21", "person in the arts", "person", "could not find mechanism"},
	{"SB184", "musician", "person", "could not find mechanism"},
	{"nm_screenTimeSegment", "screen time segment", "person", "could not find mechanism"},
	// attribute (11)
	{"CS8", "sex", "attribute", "might be implemented (unverified)"},
	{"CS4", "unit of measurement", "attribute", "implemented"},
	{"CS25", "type of profession", "attribute", "implemented"},
	{"CS37", "religion", "attribute", "could not find mechanism"},
	{"SB74", "language", "attribute", "could not find mechanism"},
	{"SB18", "political party", "attribute", "could not find mechanism"},
	{"SB461", "genre", "attribute", "implemented"},
	{"SB537", "mode of transportation", "attribute", "implemented"},
	{"SB544", "automobile model", "attribute", "implemented"},
	{"SB418", "make", "attribute", "implemented"},
	{"SB399", "content rating", "attribute", "could not find mechanism"},
	// place (5)
	{"SB48", "populated place", "place", "implemented"},
	{"SB101", "state", "place", "might be implemented (unverified)"},
	{"SB163", "country", "place", "might be implemented (unverified)"},
	{"SB370", "time zone", "place", "might be implemented (unverified)"},
	{"SB131", "county", "place", "implemented"},
	// event (5)
	{"SB471", "calendar", "event", "could not find mechanism"},
	{"SB472", "calendar event", "event", "implemented"},
	{"SB513", "activity event", "event", "implemented"},
	{"SB71", "sports game event", "event", "could not find mechanism"},
	{"SB77", "holiday event", "event", "implemented"},
	// activity (30)
	{"SB514", "transportation activity", "activity", "implemented"},
	{"SB515", "working activity", "activity", "implemented"},
	{"SB516", "meeting activity", "activity", "implemented"},
	{"SB517", "eating activity", "activity", "might be implemented (unverified)"},
	{"SB518", "watching TV activity", "activity", "might be implemented (unverified)"},
	{"SB519", "communicating activity", "activity", "implemented"},
	{"SB520", "shopping activity", "activity", "implemented"},
	{"SB523", "traveling activity", "activity", "might be implemented (unverified)"},
	{"SB524", "reading activity", "activity", "implemented"},
	{"SB525", "vacationing activity", "activity", "implemented"},
	{"SB526", "gaming activity", "activity", "implemented"},
	{"SB527", "socializing activity", "activity", "might be implemented (unverified)"},
	{"SB528", "waking up activity", "activity", "might be implemented (unverified)"},
	{"SB529", "going to bed activity", "activity", "might be implemented (unverified)"},
	{"SB522", "sleeping activity", "activity", "implemented"},
	{"SB530", "exercising activity", "activity", "implemented"},
	{"SB521", "mindfulness activity", "activity", "might be implemented (unverified)"},
	{"SB531", "on the phone activity", "activity", "implemented"},
	{"SB532", "facetime activity", "activity", "implemented"},
	{"SB533", "walking activity", "activity", "implemented"},
	{"SB534", "running activity", "activity", "implemented"},
	{"SB535", "bicycling activity", "activity", "implemented"},
	{"SB536", "hiking activity", "activity", "could not find mechanism"},
	{"SB722", "commute activity", "activity", "implemented"},
	{"SB723", "dining activity", "activity", "implemented"},
	{"SB726", "attending a performance activity", "activity", "implemented"},
	{"SB732", "stationary activity", "activity", "implemented"},
	{"SB764", "location visit activity", "activity", "implemented"},
	{"SB784", "driving activity", "activity", "could not find mechanism"},
	{"SB786", "behavior activity", "activity", "could not find mechanism"},
	{"SB394", "sports activity", "activity", "implemented"},
	// activity type (25)
	{"SB116", "type of activity", "activity type", "might be implemented (unverified)"},
	{"SB545", "transportation activity type", "activity type", "could not find mechanism"},
	{"SB546", "working activity type", "activity type", "could not find mechanism"},
	{"SB547", "meeting activity type", "activity type", "could not find mechanism"},
	{"SB548", "eating activity type", "activity type", "could not find mechanism"},
	{"SB549", "watching media activity type", "activity type", "could not find mechanism"},
	{"SB550", "communicating activity type", "activity type", "could not find mechanism"},
	{"SB551", "shopping activity type", "activity type", "could not find mechanism"},
	{"SB552", "mindful activity type", "activity type", "could not find mechanism"},
	{"SB553", "sleep activity type", "activity type", "could not find mechanism"},
	{"SB554", "travel activity type", "activity type", "could not find mechanism"},
	{"SB555", "reading activity type", "activity type", "could not find mechanism"},
	{"SB556", "vacation activity type", "activity type", "could not find mechanism"},
	{"SB557", "social activity type", "activity type", "could not find mechanism"},
	{"SB558", "waking activity type", "activity type", "could not find mechanism"},
	{"SB559", "going to bed activity type", "activity type", "could not find mechanism"},
	{"SB560", "phone call activity type", "activity type", "could not find mechanism"},
	{"SB561", "facetime activity type", "activity type", "could not find mechanism"},
	{"SB562", "walking activity type", "activity type", "could not find mechanism"},
	{"SB563", "running activity type", "activity type", "could not find mechanism"},
	{"SB564", "biking activity type", "activity type", "could not find mechanism"},
	{"SB565", "hiking activity type", "activity type", "could not find mechanism"},
	{"SB483", "type of physical exercise", "activity type", "could not find mechanism"},
	{"SB721", "commute activity type", "activity type", "could not find mechanism"},
	{"SB785", "driving activity type", "activity type", "could not find mechanism"},
	{"SB787", "behavior activity type", "activity type", "could not find mechanism"},
	// transport (8)
	{"SB425", "vehicle", "transport", "implemented"},
	{"SB543", "on foot", "transport", "could not find mechanism"},
	{"SB335", "automobile", "transport", "implemented"},
	{"SB538", "plane", "transport", "could not find mechanism"},
	{"SB539", "bicycle", "transport", "could not find mechanism"},
	{"SB358", "ship", "transport", "could not find mechanism"},
	{"SB541", "bus", "transport", "could not find mechanism"},
	{"SB542", "locomotive train", "transport", "could not find mechanism"},
	// media (7)
	{"SB51", "work", "media", "implemented"},
	{"SB405", "media work", "media", "implemented"},
	{"SB44", "song", "media", "implemented"},
	{"SB138", "album", "media", "could not find mechanism"},
	{"SB85", "musical work", "media", "implemented"},
	{"SB183", "movie", "media", "could not find mechanism"},
	{"SB295", "television program", "media", "could not find mechanism"},
	// animal (7)
	{"SB49", "animal", "animal", "could not find mechanism"},
	{"nm_pet", "pet", "animal", "could not find mechanism"},
	{"nm_dog", "dog", "animal", "could not find mechanism"},
	{"nm_cat", "cat", "animal", "could not find mechanism"},
	{"nm_myPet", "my pet", "animal", "could not find mechanism"},
	{"nm_myDog", "my dog", "animal", "could not find mechanism"},
	{"nm_myCat", "my cat", "animal", "could not find mechanism"},
	// commerce (7)
	{"nm_walletOrder", "wallet order", "commerce", "implemented"},
	{"nm_walletTransaction", "wallet transaction", "commerce", "could not find mechanism"},
	{"nm_payment", "payment", "commerce", "implemented"},
	{"nm_transaction", "transaction", "commerce", "implemented"},
	{"nm_email", "email", "commerce", "implemented"},
	{"nm_shippingInfo", "shippingInfo", "commerce", "could not find mechanism"},
	{"nm_shipmentFullFillmentInfo", "shipment full fillment info", "commerce", "implemented"},
	// edge (46)
	{"CS29", "location relationship type", "edge", "implemented"},
	{"CS97", "address relationship type", "edge", "implemented"},
	{"nm_topicRelationshipType", "topic relationship type", "edge", "might be implemented (unverified)"},
	{"CS1", "relationship type", "edge", "implemented"},
	{"nm_senderRelationshipType", "sender relationship type", "edge", "might be implemented (unverified)"},
	{"nm_calendarRelationshipType", "calendar relationship type", "edge", "implemented"},
	{"nm_organizerRelationshipType", "organizer relationship type", "edge", "could not find mechanism"},
	{"CS105", "text relationship type", "edge", "could not find mechanism"},
	{"CS101", "language relationship type", "edge", "could not find mechanism"},
	{"CS22", "identifier relationship type", "edge", "implemented"},
	{"CS2", "image relationship type", "edge", "might be implemented (unverified)"},
	{"CS13", "membership relationship type", "edge", "implemented"},
	{"CS165", "employment relationship type", "edge", "implemented"},
	{"CS154", "time zone relationship type", "edge", "might be implemented (unverified)"},
	{"CS3", "quantity relationship type", "edge", "implemented"},
	{"CS36", "religion relationship type", "edge", "could not find mechanism"},
	{"CS167", "interest relationship type", "edge", "could not find mechanism"},
	{"CS38", "participation relationship type", "edge", "implemented"},
	{"nm_calendarSourceRelationshipType", "calendar source relationship type", "edge", "implemented"},
	{"CS168", "agent affiliation relationship type", "edge", "implemented"},
	{"nm_dateRelationshipType", "date relationship type", "edge", "implemented"},
	{"nm_administratorRelationshipType", "administrator relationship type", "edge", "could not find mechanism"},
	{"nm_visualIdentifierRelationshipType", "visual identifier relationship type", "edge", "implemented"},
	{"nm_songRelationshipType", "song relationship type", "edge", "implemented"},
	{"nm_softwareRelationshipType", "software relationship type", "edge", "implemented"},
	{"CS173", "event relationship type", "edge", "could not find mechanism"},
	{"nm_localizedNameRelationshipType", "localized name relationship type", "edge", "implemented"},
	{"nm_confidenceRelationshipType", "confidence relationship type", "edge", "implemented"},
	{"CS31", "performance relationship type", "edge", "could not find mechanism"},
	{"CS15", "part whole relationship type", "edge", "could not find mechanism"},
	{"CS113", "rating relationship type", "edge", "could not find mechanism"},
	{"CS166", "group relationship type", "edge", "implemented"},
	{"nm_livingBeingRelationship", "living being relationship", "edge", "implemented"},
	{"nm_entityAliasRelationshipType", "entity alias relationship type", "edge", "implemented"},
	{"nm_extractionTagRelationshipType", "extraction tag relationship type", "edge", "implemented"},
	{"nm_orderEmailRelationshipType", "order email relationship type", "edge", "implemented"},
	{"nm_orderTransactionRelationshipType", "order transaction relationship type", "edge", "implemented"},
	{"nm_orderShippingInfoRelationshipType", "order shipping info relationship type", "edge", "implemented"},
	{"nm_entityOrganizationRelationshipType", "entity organization relationship type", "edge", "implemented"},
	{"nm_shipmentFullFillmentEmailRelationshipType", "shipment full fillment email relationship type", "edge", "implemented"},
	{"nm_orderShipmentFullFillmentRelationshipType", "order shipment full fillment relationship type", "edge", "implemented"},
	{"nm_orderPaymentRelationshipType", "order payment relationship type", "edge", "implemented"},
	{"nm_fromToEmailRelationshipType", "from to email relationship type", "edge", "could not find mechanism"},
	{"nm_orderPersonRelationshipType", "order person relationship type", "edge", "implemented"},
	{"nm_shipmentPersonRelationshipType", "shipment person relationship type", "edge", "implemented"},
	{"nm_emailInfoRelationshipType", "email info relationship type", "edge", "could not find mechanism"},
}

type applePredRaw struct{ id, label, source, method, mech string }

var applePredicatesRaw = []applePredRaw{
	{"PS1", "what kind of thing this is", "every source", "Rule", "implemented"},
	{"PS10", "genre", "Apple world knowledge", "Apple-shipped", "implemented"},
	{"PS102", "this record is the same person/thing as that one", "every source", "Embedding model", "implemented"},
	{"PS106", "duration", "Calendar, Suggestions", "Copied", "could not find mechanism"},
	{"PS107", "has location", "Calendar, location history", "Copied", "might be implemented (unverified)"},
	{"PS123", "part in part-whole relationship", "Apple world knowledge", "Apple-shipped", "could not find mechanism"},
	{"PS132", "has religion", "Contacts", "Copied", "could not find mechanism"},
	{"PS133", "religion", "Apple world knowledge", "Apple-shipped", "could not find mechanism"},
	{"PS135", "area", "Apple world knowledge", "Apple-shipped", "could not find mechanism"},
	{"PS137", "performed by", "Apple world knowledge", "Apple-shipped", "could not find mechanism"},
	{"PS143", "attendee identifier", "Calendar, Suggestions", "Copied", "might be implemented (unverified)"},
	{"PS144", "took part in an event", "Calendar, Photos", "Copied", "could not find mechanism"},
	{"PS147", "population", "Apple world knowledge", "Apple-shipped", "could not find mechanism"},
	{"PS16", "performer", "Apple world knowledge", "Apple-shipped", "could not find mechanism"},
	{"PS194", "published by", "Apple world knowledge", "Apple-shipped", "implemented"},
	{"PS2", "unit", "Apple world knowledge", "Apple-shipped", "implemented"},
	{"PS225", "has location relationship", "Calendar, location history", "Copied", "implemented"},
	{"PS232", "has attendee", "Calendar, Suggestions", "Copied", "implemented"},
	{"PS248", "starting location", "Calendar, location history", "Copied", "implemented"},
	{"PS249", "end location", "Calendar, location history", "Copied", "implemented"},
	{"PS29", "start time", "Calendar, Suggestions", "Copied", "implemented"},
	{"PS296", "URL", "Calendar, apps, world knowledge", "Copied", "implemented"},
	{"PS3", "quantity", "Apple world knowledge", "Apple-shipped", "implemented"},
	{"PS30", "end time", "Calendar, Suggestions", "Copied", "implemented"},
	{"PS312", "first name", "Contacts", "Copied", "implemented"},
	{"PS313", "username / handle", "Contacts, Family Sharing", "Copied", "implemented"},
	{"PS314", "sport", "Apple world knowledge", "Apple-shipped", "could not find mechanism"},
	{"PS315", "postal code", "Contacts, order emails", "Copied", "implemented"},
	{"PS316", "full street address", "Contacts, order emails", "Copied", "might be implemented (unverified)"},
	{"PS317", "has address", "Contacts, order emails", "Copied", "implemented"},
	{"PS324", "has language", "Contacts", "Copied", "could not find mechanism"},
	{"PS325", "languages spoken", "Contacts", "Copied", "could not find mechanism"},
	{"PS33", "name", "Contacts", "Copied", "implemented"},
	{"PS335", "text value", "Calendar, apps, world knowledge", "Copied", "could not find mechanism"},
	{"PS336", "official name", "Apple world knowledge", "Apple-shipped", "could not find mechanism"},
	{"PS34", "gender identity", "Contacts", "Copied", "might be implemented (unverified)"},
	{"PS343", "demonym", "Apple world knowledge", "Apple-shipped", "could not find mechanism"},
	{"PS35", "citizen of", "Apple world knowledge", "Apple-shipped", "could not find mechanism"},
	{"PS358", "has rating", "Apple world knowledge", "Apple-shipped", "could not find mechanism"},
	{"PS359", "rating", "Apple world knowledge", "Apple-shipped", "could not find mechanism"},
	{"PS36", "birth name", "Contacts", "Copied", "implemented"},
	{"PS37", "date of birth", "Contacts", "Copied", "implemented"},
	{"PS374", "has member", "Contacts, Family Sharing", "Copied", "implemented"},
	{"PS392", "last updated", "Calendar, Suggestions", "Copied", "implemented"},
	{"PS396", "external identifier, its value, its type", "every source", "Joined", "implemented"},
	{"PS4", "width", "Apple world knowledge", "Apple-shipped", "might be implemented (unverified)"},
	{"PS406", "email address", "Contacts, order emails", "Copied", "implemented"},
	{"PS407", "phone number", "Contacts, order emails", "Copied", "implemented"},
	{"PS424", "member of political party", "Apple world knowledge", "Apple-shipped", "could not find mechanism"},
	{"PS441", "family name", "Contacts", "Copied", "implemented"},
	{"PS447", "has timezone", "Calendar, location history", "Copied", "could not find mechanism"},
	{"PS448", "time zone", "Calendar, location history", "Copied", "might be implemented (unverified)"},
	{"PS45", "member of", "Contacts, Family Sharing", "Copied", "implemented"},
	{"PS450", "address country", "Contacts, order emails", "Copied", "might be implemented (unverified)"},
	{"PS453", "city", "Contacts, order emails", "Copied", "might be implemented (unverified)"},
	{"PS454", "country code ISO 3166-1 alpha-2", "Contacts, order emails", "Copied", "might be implemented (unverified)"},
	{"PS455", "administrative district", "Contacts, order emails", "Copied", "might be implemented (unverified)"},
	{"PS46", "group member identifier", "Family Sharing", "Joined", "could not find mechanism"},
	{"PS484", "official language", "Apple world knowledge", "Apple-shipped", "could not find mechanism"},
	{"PS486", "display image", "Photos, Contacts", "Copied", "could not find mechanism"},
	{"PS5", "height", "Apple world knowledge", "Apple-shipped", "might be implemented (unverified)"},
	{"PS500", "employer", "Contacts", "Copied", "implemented"},
	{"PS51", "description", "Calendar, apps, world knowledge", "Copied", "implemented"},
	{"PS515", "location within location", "Apple world knowledge", "Apple-shipped", "could not find mechanism"},
	{"PS520", "has contact information", "Contacts, order emails", "Copied", "implemented"},
	{"PS524", "native home", "Apple world knowledge", "Apple-shipped", "could not find mechanism"},
	{"PS53", "part of", "Apple world knowledge", "Apple-shipped", "could not find mechanism"},
	{"PS545", "name prefix", "Contacts", "Copied", "implemented"},
	{"PS546", "middle name", "Contacts", "Copied", "implemented"},
	{"PS547", "name suffix", "Contacts", "Copied", "implemented"},
	{"PS548", "department", "Contacts", "Copied", "implemented"},
	{"PS557", "topics a person is interested in", "Personalization Portrait", "Scored model", "could not find mechanism"},
	{"PS558", "topics a person is interested in", "Personalization Portrait", "Scored model", "could not find mechanism"},
	{"PS565", "abbreviation", "Apple world knowledge", "Apple-shipped", "could not find mechanism"},
	{"PS566", "group has part", "Apple world knowledge", "Apple-shipped", "could not find mechanism"},
	{"PS567", "seconds from GMT", "Calendar, location history", "Copied", "could not find mechanism"},
	{"PS568", "sub administrative area", "Contacts, order emails", "Copied", "implemented"},
	{"PS569", "sub locality", "Contacts, order emails", "Copied", "implemented"},
	{"PS570", "sub thoroughfare", "Contacts, order emails", "Copied", "implemented"},
	{"PS571", "latitude", "Calendar, location history", "Copied", "implemented"},
	{"PS572", "longitude", "Calendar, location history", "Copied", "implemented"},
	{"PS573", "has latitude/longitude", "Calendar, location history", "Copied", "implemented"},
	{"PS574", "previous family name", "Contacts", "Copied", "implemented"},
	{"PS578", "has organizer", "Calendar, Suggestions", "Copied", "implemented"},
	{"PS579", "on calendar", "Calendar, Suggestions", "Copied", "could not find mechanism"},
	{"PS580", "calendar event sender", "Calendar, Suggestions", "Copied", "implemented"},
	{"PS581", "second address line", "Contacts, order emails", "Copied", "implemented"},
	{"PS582", "who administers the home", "HomeKit", "Copied", "could not find mechanism"},
	{"PS586", "status", "—", "Not established", "implemented"},
	{"PS598", "a person is related to / associated with another", "Contacts, Health, Family Sharing, Screen Time, Photos", "XGBoost + rules + copied", "implemented"},
	{"PS599", "a person is related to / associated with another", "Contacts, Health, Family Sharing, Screen Time, Photos", "XGBoost + rules + copied", "could not find mechanism"},
	{"PS618", "has subevent", "Calendar, Suggestions", "Copied", "could not find mechanism"},
	{"PS66", "source URL", "Calendar, apps, world knowledge", "Copied", "implemented"},
	{"PS67", "date of death", "Apple world knowledge", "Apple-shipped", "could not find mechanism"},
	{"PS69", "external identifier, its value, its type", "every source", "Joined", "implemented"},
	{"PS7", "image file", "Photos, Contacts", "Copied", "might be implemented (unverified)"},
	{"PS709", "has driver", "CarPlay, Wallet", "Copied", "could not find mechanism"},
	{"PS710", "transportation type", "Apple world knowledge", "Apple-shipped", "could not find mechanism"},
	{"PS711", "elevation gain", "Apple world knowledge", "Apple-shipped", "could not find mechanism"},
	{"PS712", "terrain", "Apple world knowledge", "Apple-shipped", "implemented"},
	{"PS713", "fuel consumed", "Apple world knowledge", "Apple-shipped", "could not find mechanism"},
	{"PS714", "highway mileage", "Apple world knowledge", "Apple-shipped", "could not find mechanism"},
	{"PS715", "city mileage", "Apple world knowledge", "Apple-shipped", "could not find mechanism"},
	{"PS716", "make of automobile", "CarPlay, Wallet", "Copied", "could not find mechanism"},
	{"PS717", "model of automobile", "CarPlay, Wallet", "Copied", "could not find mechanism"},
	{"PS718", "two events happened together", "Photos, Calendar", "Rule", "could not find mechanism"},
	{"PS719", "kind of activity", "Health, motion sensors", "Rule", "could not find mechanism"},
	{"PS72", "external identifier, its value, its type", "every source", "Joined", "implemented"},
	{"PS73", "occupation", "Contacts, Apple world knowledge", "Copied", "implemented"},
	{"PS730", "sub premises", "Contacts, order emails", "Copied", "might be implemented (unverified)"},
	{"PS731", "administrative area code", "Contacts, order emails", "Copied", "might be implemented (unverified)"},
	{"PS733", "label the user gave a place or thing", "Calendar, Suggestions", "Copied", "might be implemented (unverified)"},
	{"PS734", "participant obligation", "Calendar, Suggestions", "Copied", "could not find mechanism"},
	{"PS740", "contact label", "Contacts", "Copied", "implemented"},
	{"PS741", "has event", "Calendar, Suggestions", "Copied", "could not find mechanism"},
	{"PS747", "behavior category", "Behavioral history", "Rule", "could not find mechanism"},
	{"PS75", "has mother", "—", "Not established", "could not find mechanism"},
	{"PS753", "group includes individual", "Family Sharing", "Copied", "implemented"},
	{"PS76", "has father", "—", "Not established", "could not find mechanism"},
	{"PS77", "spouse", "—", "Not established", "could not find mechanism"},
	{"PS78", "has image", "Photos, Contacts", "Copied", "could not find mechanism"},
	{"PS79", "has child", "—", "Not established", "could not find mechanism"},
	{"PS83", "length", "Apple world knowledge", "Apple-shipped", "could not find mechanism"},
	{"PS85", "role held", "Apple world knowledge", "Apple-shipped", "could not find mechanism"},
	{"PS89", "another name the same person is known by", "Contacts, Photos", "Embedding model", "implemented"},
	{"PS99", "net worth", "Apple world knowledge", "Apple-shipped", "could not find mechanism"},
	{"nm_QID", "Wikidata id", "Apple world knowledge", "Apple-shipped", "implemented"},
	{"nm_UMC_ID", "UMC ID", "Apple Music", "Copied", "implemented"},
	{"nm_accountInstitution", "account institution", "Wallet, order emails", "Copied", "implemented"},
	{"nm_accountLastFourDigits", "account last four digits", "Wallet, order emails", "Copied", "implemented"},
	{"nm_accountType", "account type", "Wallet, order emails", "Copied", "implemented"},
	{"nm_accuracy", "accuracy", "Location history", "Copied", "could not find mechanism"},
	{"nm_activityCategory", "event type pulled out of text", "Mail, Messages, app content", "Rule", "could not find mechanism"},
	{"nm_addressString", "address string", "Calendar, Suggestions", "Copied", "implemented"},
	{"nm_age", "age", "Family Sharing", "Copied", "implemented"},
	{"nm_albumIdentifier", "album identifier", "Apple Music", "Copied", "implemented"},
	{"nm_albumName", "song album name", "Apple Music, app usage", "Copied", "implemented"},
	{"nm_amount", "amount", "Wallet, order emails", "Copied", "implemented"},
	{"nm_anniversary", "anniversary", "Contacts", "Copied", "implemented"},
	{"nm_applicationIdentifier", "application identifier", "Installed apps", "Copied", "implemented"},
	{"nm_artistIdentifier", "artist identifier", "Apple Music", "Copied", "implemented"},
	{"nm_artistName", "song artist name", "Apple Music, app usage", "Copied", "implemented"},
	{"nm_associationReason", "which mechanism named this face", "Photos faces", "Rule", "implemented"},
	{"nm_availability", "availability", "Calendar, Suggestions", "Copied", "could not find mechanism"},
	{"nm_birthdayContactID", "birthday contact id", "Contacts", "Joined", "could not find mechanism"},
	{"nm_bundleID", "bundle id", "Installed apps", "Copied", "implemented"},
	{"nm_calendarType", "calendar type", "Calendar, Suggestions", "Copied", "could not find mechanism"},
	{"nm_carrierName", "carrier name", "Wallet passes, Calendar", "Copied", "implemented"},
	{"nm_category", "topic scored from what you read and write", "Personalization Portrait", "Scored model", "could not find mechanism"},
	{"nm_cnFavoriteActionType", "contact favorite action type", "Contacts", "Copied", "implemented"},
	{"nm_confidence", "how sure the system is", "whichever engine wrote the fact", "Rule", "implemented"},
	{"nm_confirmationConfidence", "how sure a name match is", "Contacts, Photos", "Embedding model", "implemented"},
	{"nm_creationDate", "creation date", "Calendar, Suggestions", "Copied", "implemented"},
	{"nm_dates", "dates", "Calendar, Suggestions", "Copied", "implemented"},
	{"nm_documentID", "document ID", "—", "Not established", "could not find mechanism"},
	{"nm_dynamicCategory", "topic scored from what you read and write", "Personalization Portrait", "Scored model", "could not find mechanism"},
	{"nm_emailFrom", "email from", "Order emails", "Copied", "implemented"},
	{"nm_emailIdentifier", "email identifier", "Order emails", "Joined", "implemented"},
	{"nm_emailReplyTo", "email reply to", "Order emails", "Copied", "implemented"},
	{"nm_emailTo", "email to", "Order emails", "Copied", "could not find mechanism"},
	{"nm_emailType", "email type", "Order emails", "Copied", "implemented"},
	{"nm_entityAliasRelationship", "link joining two names for one person", "Contacts, Photos", "Embedding model", "implemented"},
	{"nm_exactVersion", "exact version", "Installed apps", "Copied", "could not find mechanism"},
	{"nm_extensionHostId", "extension host id", "Installed apps", "Copied", "could not find mechanism"},
	{"nm_familyMemberType", "family member type", "Family Sharing", "Copied", "implemented"},
	{"nm_file", "file", "Installed apps", "Copied", "could not find mechanism"},
	{"nm_flightNumber", "flight number", "Wallet passes, Calendar", "Copied", "implemented"},
	{"nm_handleIdentifier", "handle identifier", "Messages, Mail", "Joined", "implemented"},
	{"nm_handles", "the phone numbers and emails a person uses", "Contacts", "Copied", "might be implemented (unverified)"},
	{"nm_hasAdministrator", "has administrator", "HomeKit", "Copied", "could not find mechanism"},
	{"nm_hasCalendar", "has calendar", "Calendar, Suggestions", "Copied", "could not find mechanism"},
	{"nm_hasConfidence", "per-source trust weight", "Downloadable Apple asset", "Apple-shipped", "implemented"},
	{"nm_hasDate", "has date", "Calendar, Suggestions", "Copied", "implemented"},
	{"nm_hasDomain", "which classes a predicate applies to", "The ontology itself", "Apple-shipped", "could not find mechanism"},
	{"nm_hasEmail", "has email", "Order emails", "Copied", "implemented"},
	{"nm_hasExtractionTags", "has extraction tags", "Ingest lane", "Rule", "could not find mechanism"},
	{"nm_hasFavoriteSportsTeam", "has favorite sports team", "App usage, Apple Music", "Copied", "could not find mechanism"},
	{"nm_hasField", "has field", "—", "Not established", "implemented"},
	{"nm_hasLocalizedName", "has localized name", "Apple world knowledge", "Apple-shipped", "implemented"},
	{"nm_hasMerchant", "has merchant", "Wallet, order emails", "Copied", "implemented"},
	{"nm_hasPayment", "has payment", "Wallet, order emails", "Copied", "implemented"},
	{"nm_hasPerson", "has person", "Order emails", "Copied", "implemented"},
	{"nm_hasProfileImage", "has profile image", "Contacts", "Copied", "implemented"},
	{"nm_hasRange", "which classes a predicate applies to", "The ontology itself", "Apple-shipped", "implemented"},
	{"nm_hasSender", "has sender", "Order emails", "Copied", "might be implemented (unverified)"},
	{"nm_hasShipmentFullFillment", "has shipment full fillment", "Wallet, order emails", "Copied", "implemented"},
	{"nm_hasShippingInfo", "has shipping info", "Wallet, order emails", "Copied", "could not find mechanism"},
	{"nm_hasSoftware", "has software", "Installed apps", "Copied", "implemented"},
	{"nm_hasSong", "has song", "Apple Music, app usage", "Copied", "implemented"},
	{"nm_hasSource", "has source", "Calendar, Suggestions", "Copied", "could not find mechanism"},
	{"nm_hasTopic", "topic scored from what you read and write", "Personalization Portrait", "Scored model", "might be implemented (unverified)"},
	{"nm_hasTransaction", "has transaction", "Wallet, order emails", "Copied", "implemented"},
	{"nm_hasVisualIdentifier", "the face cluster for a person", "Photos faces", "Face clustering", "implemented"},
	{"nm_homeTeam", "home team", "Apple Music, app usage", "Copied", "could not find mechanism"},
	{"nm_iataCode", "iata code", "Wallet passes, Calendar", "Copied", "implemented"},
	{"nm_image_bytes", "image bytes", "Photos, Contacts", "Copied", "implemented"},
	{"nm_imputedAnniversary", "imputed anniversary", "Contacts, behavior", "Unknown model", "implemented"},
	{"nm_imputedBirthday", "birthday filled in rather than told — and the one the system actually reads", "Photos age estimate (candidate)", "Unknown model", "implemented"},
	{"nm_imputedCreationDate", "timestamp filled in or normalized", "Calendar, Suggestions", "Rule", "implemented"},
	{"nm_imputedEndTime", "timestamp filled in or normalized", "Calendar, Suggestions", "Rule", "implemented"},
	{"nm_imputedInferredBirthday", "birthday filled in rather than told — and the one the system actually reads", "Photos age estimate (candidate)", "Unknown model", "implemented"},
	{"nm_imputedLastUpdated", "timestamp filled in or normalized", "Calendar, Suggestions", "Rule", "implemented"},
	{"nm_imputedNonGregorianBirthday", "imputed non-gregorian birthday", "Contacts, behavior", "Unknown model", "implemented"},
	{"nm_imputedOccurrenceDate", "timestamp filled in or normalized", "Calendar, Suggestions", "Rule", "implemented"},
	{"nm_imputedStartTime", "timestamp filled in or normalized", "Calendar, Suggestions", "Rule", "implemented"},
	{"nm_inferenceFeedbackID", "handle marking a machine-made claim", "The kinship classifier", "XGBoost", "implemented"},
	{"nm_inferredBirthday", "birthday filled in rather than told — and the one the system actually reads", "Photos age estimate (candidate)", "Unknown model", "implemented"},
	{"nm_inferredDeviceUser", "owner worked out rather than declared", "Behavioral history", "Unknown model", "implemented"},
	{"nm_inferredSemanticLabel", "what a place is for, e.g. home, gym", "Location history", "Unknown model", "implemented"},
	{"nm_intentPersonId", "person as an app referred to them", "App donations", "Joined", "implemented"},
	{"nm_isAllDay", "is all day", "Calendar, Suggestions", "Copied", "implemented"},
	{"nm_isCurrentUser", "this person is the phone's owner", "Contacts me-card", "Rule", "implemented"},
	{"nm_isDetached", "is detached", "Calendar, Suggestions", "Copied", "could not find mechanism"},
	{"nm_isFavorite", "is favorite", "Contacts", "Copied", "implemented"},
	{"nm_isStarting", "is starting", "Calendar, Suggestions", "Copied", "could not find mechanism"},
	{"nm_isSubscribed", "subscribed status", "Calendar, Suggestions", "Copied", "could not find mechanism"},
	{"nm_launchReason", "launch reason", "App usage, Screen Time", "Copied", "could not find mechanism"},
	{"nm_launchType", "launch type", "App usage, Screen Time", "Copied", "could not find mechanism"},
	{"nm_league_name", "league name", "Apple Music, app usage", "Copied", "implemented"},
	{"nm_listening_confidence", "per-language competence on a relationship", "—", "Not established", "might be implemented (unverified)"},
	{"nm_localeIdentifier", "locale identifier", "System settings", "Copied", "implemented"},
	{"nm_mappedTopic", "topic scored from what you read and write", "Personalization Portrait", "Scored model", "could not find mechanism"},
	{"nm_merchantName", "merchant name", "Wallet, order emails", "Copied", "implemented"},
	{"nm_naturalLanguageEventTypeIdentifier", "event type pulled out of text", "Mail, Messages, app content", "Rule", "could not find mechanism"},
	{"nm_nonGregorianBirthday", "non-gregorian birthday", "Contacts", "Copied", "implemented"},
	{"nm_occurrenceDate", "occurrence date", "Calendar, Suggestions", "Copied", "implemented"},
	{"nm_orderContentType", "order content type", "Wallet, order emails", "Copied", "implemented"},
	{"nm_orderNumber", "order number", "Wallet, order emails", "Copied", "implemented"},
	{"nm_organizationIdentifier", "organization identifier", "Contacts, Wallet", "Joined", "implemented"},
	{"nm_parentBundleId", "parent bundle id", "Installed apps", "Copied", "could not find mechanism"},
	{"nm_paymentIdentifier", "payment identifier", "Wallet", "Joined", "implemented"},
	{"nm_paymentMethodIsApplePay", "payment method is apple pay", "Wallet, order emails", "Copied", "implemented"},
	{"nm_personIdentifier", "who a relationship points at", "the other end of a relationship", "Joined", "implemented"},
	{"nm_personType", "what kind of thing Photos detected", "Photos", "Rule", "implemented"},
	{"nm_phoneticFamilyName", "phonetic family name", "Contacts", "Copied", "implemented"},
	{"nm_phoneticFirstName", "phonetic first name", "Contacts", "Copied", "implemented"},
	{"nm_phoneticMiddleName", "phonetic middle name", "Contacts", "Copied", "implemented"},
	{"nm_phoneticName", "phonetic name", "Contacts", "Copied", "implemented"},
	{"nm_photosDetectionType", "what kind of thing Photos detected", "Photos", "Rule", "implemented"},
	{"nm_poiType", "kind of place", "Maps, location history", "Copied", "could not find mechanism"},
	{"nm_reading_confidence", "per-language competence on a relationship", "—", "Not established", "might be implemented (unverified)"},
	{"nm_relationship", "a person is related to / associated with another", "Contacts, Health, Family Sharing, Screen Time, Photos", "XGBoost + rules + copied", "implemented"},
	{"nm_relationshipLabel", "the relationship word, e.g. sister, coworker", "Contacts, Health, Family Sharing", "XGBoost + copied", "implemented"},
	{"nm_role", "role", "—", "Not established", "could not find mechanism"},
	{"nm_screenTimeSegmentPerson", "screen time segment person", "Screen Time", "Copied", "could not find mechanism"},
	{"nm_senderDomain", "sender domain", "Order emails", "Copied", "implemented"},
	{"nm_senderIdentifier", "sender identifier", "Messages, Mail", "Joined", "might be implemented (unverified)"},
	{"nm_sgEventType", "event type worked out from an app or email", "Suggestions", "Rule", "implemented"},
	{"nm_shipmentFullFillmentIdentifier", "shipment full fillment identifier", "Wallet", "Joined", "implemented"},
	{"nm_shippingMethod", "shipping method", "Wallet, order emails", "Copied", "implemented"},
	{"nm_shippingStatus", "shipping status", "Wallet, order emails", "Copied", "implemented"},
	{"nm_shortVersion", "short version", "Installed apps", "Copied", "could not find mechanism"},
	{"nm_signalLabel", "signal label", "Photos", "Rule", "implemented"},
	{"nm_softwareIdentifier", "software identifier", "Installed apps", "Joined", "might be implemented (unverified)"},
	{"nm_songIdentifier", "song identifier", "Apple Music", "Copied", "could not find mechanism"},
	{"nm_sourceType", "source type", "Ingest lane", "Rule", "implemented"},
	{"nm_subject", "subject", "Order emails", "Copied", "implemented"},
	{"nm_suggestedName", "name guessed for an unnamed face", "Photos faces + Messages text", "Local LLM", "implemented"},
	{"nm_suggestedRelationshipConfidence", "guessed relationship of a face to the phone's owner", "Photos faces + Messages text", "Local LLM", "implemented"},
	{"nm_suggestedRelationshipToDeviceOwner", "guessed relationship of a face to the phone's owner", "Photos faces + Messages text", "Local LLM", "implemented"},
	{"nm_teamIdentifier", "team identifier", "Installed apps", "Joined", "implemented"},
	{"nm_toDisplayName", "display name on an email recipient", "Email", "Copied", "implemented"},
	{"nm_toEmailAddress", "to email address", "Email", "Copied", "implemented"},
	{"nm_topicIdentifier", "topic identifier", "Personalization Portrait", "Joined", "might be implemented (unverified)"},
	{"nm_totalAmount", "total amount", "Wallet, order emails", "Copied", "implemented"},
	{"nm_totalNotifications", "total notifications", "App usage, Screen Time", "Copied", "could not find mechanism"},
	{"nm_totalPickups", "total pickups", "App usage, Screen Time", "Copied", "could not find mechanism"},
	{"nm_trackingNumber", "tracking number", "Wallet, order emails", "Copied", "implemented"},
	{"nm_transactionIdentifier", "transaction identifier", "Wallet", "Joined", "implemented"},
	{"nm_transactionSource", "transaction source", "Wallet, order emails", "Copied", "implemented"},
	{"nm_transactionStatus", "transaction status", "Wallet, order emails", "Copied", "implemented"},
	{"nm_transactionType", "transaction type", "Wallet, order emails", "Copied", "implemented"},
	{"nm_uncertainty", "uncertainty", "Location history", "Copied", "could not find mechanism"},
	{"nm_unknownHandle", "unknown handle", "Messages, Mail", "Joined", "implemented"},
	{"nm_unlocalizedRelationshipLabel", "the relationship word, e.g. sister, coworker", "Contacts, Health, Family Sharing", "XGBoost + copied", "implemented"},
	{"nm_virtualConferenceURL", "virtual conference URL", "Calendar, Suggestions", "Copied", "implemented"},
	{"nm_visualIdentifier", "the face cluster for a person", "Photos faces", "Face clustering", "implemented"},
	{"nm_writing_confidence", "per-language competence on a relationship", "—", "Not established", "might be implemented (unverified)"},
}

// appleFeatureNames is the 144 features in EntityTagging_Family.mlmodelc's
// metadata.json, in their exact 0..143 order: 0..19 identity, 20..59
// communication, 60..143 photos.
var appleFeatureNames = [144]string{
	// 0..19: identity
	"isVIP",
	"sharesWorkAddress",
	"hasSamePrivateEmailDomain",
	"hasBirthday",
	"hasSameEmailDomains",
	"sameLastName",
	"hasSamePublicEmailDomain",
	"photosPersonAge",
	"isFavorite",
	"hasSameEduEmailDomain",
	"hasSameWorkEmailDomain",
	"hasNickname",
	"allowListedNameMatch",
	"ageDifferenceCategory",
	"nameContainsEmoji",
	"nicknameContainsEmoji",
	"sharesHomeAddress",
	"allowListedNicknameMatch",
	"photosAgeDifferenceCategory",
	"iCloudFamilyMember",
	// 20..59: communication
	"personOverallPopularity",
	"personLongTermPopularity",
	"personPopularityGivenMonday",
	"personPopularityGivenTuesday",
	"personPopularityGivenWednesday",
	"personPopularityGivenThursday",
	"personPopularityGivenFriday",
	"personPopularityGivenSaturday",
	"personPopularityGivenSunday",
	"mondayPopularityGivenPerson",
	"tuesdayPopularityGivenPerson",
	"wednesdayPopularityGivenPerson",
	"thursdayPopularityGivenPerson",
	"fridayPopularityGivenPerson",
	"saturdayPopularityGivenPerson",
	"sundayPopularityGivenPerson",
	"personPosteriorProbabilityGivenMonday",
	"personPosteriorProbabilityGivenTuesday",
	"personPosteriorProbabilityGivenWednesday",
	"personPosteriorProbabilityGivenThursday",
	"personPosteriorProbabilityGivenFriday",
	"personPosteriorProbabilityGivenSaturday",
	"personPosteriorProbabilityGivenSunday",
	"personPopularityGivenMorning",
	"personPopularityGivenAfternoon",
	"personPopularityGivenEvening",
	"personPopularityGivenNight",
	"personPopularityGivenHome",
	"personPopularityGivenWork",
	"homePopularityGivenPerson",
	"workPopularityGivenPerson",
	"personPosteriorProbabilityGivenHome",
	"personPosteriorProbabilityGivenWork",
	"personPopularityGivenWeekday",
	"personPopularityGivenWeekend",
	"weekdayPopularityGivenPerson",
	"weekendPopularityGivenPerson",
	"top1PersonInteractionMechanism",
	"top2PersonInteractionMechanism",
	"top3PersonInteractionMechanism",
	// 60..143: photos
	"photosPersonOverallPopularity",
	"photosPersonLongTermPopularity",
	"photosPersonPopularityGivenMonday",
	"mondayPopularityGivenPhotosPerson",
	"photosPersonPosteriorProbabilityGivenMonday",
	"photosPersonPopularityGivenTuesday",
	"tuesdayPopularityGivenPhotosPerson",
	"photosPersonPosteriorProbabilityGivenTuesday",
	"photosPersonPopularityGivenWednesday",
	"wednesdayPopularityGivenPhotosPerson",
	"photosPersonPosteriorProbabilityGivenWednesday",
	"photosPersonPopularityGivenThursday",
	"thursdayPopularityGivenPhotosPerson",
	"photosPersonPosteriorProbabilityGivenThursday",
	"photosPersonPopularityGivenFriday",
	"fridayPopularityGivenPhotosPerson",
	"photosPersonPosteriorProbabilityGivenFriday",
	"photosPersonPopularityGivenSaturday",
	"saturdayPopularityGivenPhotosPerson",
	"photosPersonPosteriorProbabilityGivenSaturday",
	"photosPersonPopularityGivenSunday",
	"sundayPopularityGivenPhotosPerson",
	"photosPersonPosteriorProbabilityGivenSunday",
	"photosPersonPopularityGivenMorning",
	"morningPopularityGivenPhotosPerson",
	"photosPersonPosteriorProbabilityGivenMorning",
	"photosPersonPopularityGivenAfternoon",
	"afternoonPopularityGivenPhotosPerson",
	"photosPersonPosteriorProbabilityGivenAfternoon",
	"photosPersonPopularityGivenEvening",
	"eveningPopularityGivenPhotosPerson",
	"photosPersonPosteriorProbabilityGivenEvening",
	"photosPersonPopularityGivenNight",
	"nightPopularityGivenPhotosPerson",
	"photosPersonPosteriorProbabilityGivenNight",
	"photosPersonPosteriorProbabilityGivenMondayMorning",
	"photosPersonPosteriorProbabilityGivenMondayAfternoon",
	"photosPersonPosteriorProbabilityGivenMondayEvening",
	"photosPersonPosteriorProbabilityGivenMondayNight",
	"photosPersonPosteriorProbabilityGivenTuesdayMorning",
	"photosPersonPosteriorProbabilityGivenTuesdayAfternoon",
	"photosPersonPosteriorProbabilityGivenTuesdayEvening",
	"photosPersonPosteriorProbabilityGivenTuesdayNight",
	"photosPersonPosteriorProbabilityGivenWednesdayMorning",
	"photosPersonPosteriorProbabilityGivenWednesdayAfternoon",
	"photosPersonPosteriorProbabilityGivenWednesdayEvening",
	"photosPersonPosteriorProbabilityGivenWednesdayNight",
	"photosPersonPosteriorProbabilityGivenThursdayMorning",
	"photosPersonPosteriorProbabilityGivenThursdayAfternoon",
	"photosPersonPosteriorProbabilityGivenThursdayEvening",
	"photosPersonPosteriorProbabilityGivenThursdayNight",
	"photosPersonPosteriorProbabilityGivenFridayMorning",
	"photosPersonPosteriorProbabilityGivenFridayAfternoon",
	"photosPersonPosteriorProbabilityGivenFridayEvening",
	"photosPersonPosteriorProbabilityGivenFridayNight",
	"photosPersonPosteriorProbabilityGivenSaturdayMorning",
	"photosPersonPosteriorProbabilityGivenSaturdayAfternoon",
	"photosPersonPosteriorProbabilityGivenSaturdayEvening",
	"photosPersonPosteriorProbabilityGivenSaturdayNight",
	"photosPersonPosteriorProbabilityGivenSundayMorning",
	"photosPersonPosteriorProbabilityGivenSundayAfternoon",
	"photosPersonPosteriorProbabilityGivenSundayEvening",
	"photosPersonPosteriorProbabilityGivenSundayNight",
	"photosPersonPopularityGivenHome",
	"homePopularityGivenPhotosPerson",
	"photosPersonPosteriorProbabilityGivenHome",
	"photosPersonPopularityGivenWork",
	"workPopularityGivenPhotosPerson",
	"photosPersonPosteriorProbabilityGivenWork",
	"photosPersonPopularityGivenWeekday",
	"weekdayPopularityGivenPhotosPerson",
	"photosPersonPosteriorProbabilityGivenWeekday",
	"photosPersonPopularityGivenWeekend",
	"weekendPopularityGivenPhotosPerson",
	"photosPersonPosteriorProbabilityGivenWeekend",
	"photosPersonPosteriorProbabilityGivenWeekdayMorning",
	"photosPersonPosteriorProbabilityGivenWeekdayAfternoon",
	"photosPersonPosteriorProbabilityGivenWeekdayEvening",
	"photosPersonPosteriorProbabilityGivenWeekdayNight",
	"photosPersonPosteriorProbabilityGivenWeekendMorning",
	"photosPersonPosteriorProbabilityGivenWeekendAfternoon",
	"photosPersonPosteriorProbabilityGivenWeekendEvening",
	"photosPersonPosteriorProbabilityGivenWeekendNight",
	"hasWeekendWeekdayPhotosFeatures",
}

// canonicalOverrides maps specific Apple predicate codes to the assertion
// plane's own canonical relation names.
var canonicalOverrides = map[string]string{
	"PS1":                      "type",
	"PS102":                    "same",
	"PS33":                     "name",
	"PS312":                    "first_name",
	"PS441":                    "family_name",
	"PS313":                    "handle",
	"PS406":                    "email",
	"PS407":                    "phone",
	"PS37":                     "birth_date",
	"PS500":                    "employer",
	"PS45":                     "member_of",
	"PS374":                    "has_member",
	"PS598":                    "related_to",
	"PS599":                    "associated_with",
	"PS73":                     "occupation",
	"PS75":                     "mother",
	"PS76":                     "father",
	"PS77":                     "spouse",
	"PS79":                     "child",
	"PS89":                     "alias",
	"PS107":                    "location",
	"PS232":                    "attendee",
	"PS29":                     "start_time",
	"PS30":                     "end_time",
	"nm_isCurrentUser":         "is_current_user",
	"nm_relationship":          "related_to",
	"nm_relationshipLabel":     "relationship_label",
	"nm_personIdentifier":      "person",
	"nm_organizationIdentifier": "organization",
}

// edgePredicates marks Apple predicates whose object is another entity rather
// than a scalar literal.
var edgePredicates = map[string]bool{
	"PS102": true, "PS107": true, "PS123": true, "PS137": true, "PS143": true,
	"PS144": true, "PS16": true, "PS194": true, "PS225": true, "PS232": true,
	"PS248": true, "PS249": true, "PS317": true, "PS35": true, "PS374": true,
	"PS424": true, "PS45": true, "PS46": true, "PS500": true, "PS515": true,
	"PS520": true, "PS524": true, "PS53": true, "PS557": true, "PS558": true,
	"PS566": true, "PS578": true, "PS579": true, "PS580": true, "PS582": true,
	"PS598": true, "PS599": true, "PS618": true, "PS709": true, "PS718": true,
	"PS741": true, "PS75": true, "PS753": true, "PS76": true, "PS77": true,
	"PS79": true,
	"nm_entityAliasRelationship": true, "nm_hasAdministrator": true,
	"nm_hasCalendar": true, "nm_hasEmail": true, "nm_hasFavoriteSportsTeam": true,
	"nm_hasMerchant": true, "nm_hasPayment": true, "nm_hasPerson": true,
	"nm_hasSender": true, "nm_hasShipmentFullFillment": true,
	"nm_hasShippingInfo": true, "nm_hasSoftware": true, "nm_hasSong": true,
	"nm_hasTopic": true, "nm_hasTransaction": true, "nm_hasVisualIdentifier": true,
	"nm_inferredDeviceUser": true, "nm_organizationIdentifier": true,
	"nm_paymentIdentifier": true, "nm_personIdentifier": true,
	"nm_relationship": true, "nm_screenTimeSegmentPerson": true,
	"nm_senderIdentifier": true, "nm_softwareIdentifier": true,
	"nm_songIdentifier": true, "nm_topicIdentifier": true,
	"nm_transactionIdentifier": true, "nm_visualIdentifier": true,
}

var (
	appleClasses    []graphOntologyClass
	applePredicates []graphOntologyPredicate
	appleFeatures   []graphOntologyFeature
	classByID       map[string]graphOntologyClass
	predByID        map[string]graphOntologyPredicate
)

func init() {
	appleClasses = make([]graphOntologyClass, len(appleClassesRaw))
	classByID = make(map[string]graphOntologyClass, len(appleClassesRaw))
	for i, r := range appleClassesRaw {
		c := graphOntologyClass{
			ID:        r.id,
			Label:     r.label,
			Group:     r.group,
			Edge:      r.group == "edge",
			Mechanism: r.mech,
		}
		appleClasses[i] = c
		classByID[r.id] = c
	}

	applePredicates = make([]graphOntologyPredicate, len(applePredicatesRaw))
	predByID = make(map[string]graphOntologyPredicate, len(applePredicatesRaw))
	for i, r := range applePredicatesRaw {
		rel := canonicalRelation(r.id, r.label)
		p := graphOntologyPredicate{
			ID:        r.id,
			Label:     r.label,
			Relation:  rel,
			Names:     edgePredicates[r.id],
			Source:    r.source,
			Method:    r.method,
			Mechanism: r.mech,
		}
		applePredicates[i] = p
		predByID[r.id] = p
	}

	appleFeatures = make([]graphOntologyFeature, len(appleFeatureNames))
	for i, name := range appleFeatureNames {
		grp := "photos"
		switch {
		case i < 20:
			grp = "identity"
		case i < 60:
			grp = "communication"
		}
		appleFeatures[i] = graphOntologyFeature{
			Index: i,
			Name:  name,
			Group: grp,
		}
	}
}

// canonicalRelation derives a clean snake_case relation name from an Apple
// predicate code and its label.
func canonicalRelation(id, label string) string {
	if over, ok := canonicalOverrides[id]; ok {
		return over
	}
	if strings.HasPrefix(id, "nm_") {
		return camelToSnake(strings.TrimPrefix(id, "nm_"))
	}
	return slugifyLabel(label)
}

func camelToSnake(s string) string {
	var b strings.Builder
	runes := []rune(s)
	for i, r := range runes {
		if r == '_' {
			if b.Len() > 0 && !strings.HasSuffix(b.String(), "_") {
				b.WriteByte('_')
			}
			continue
		}
		if unicode.IsUpper(r) {
			if i > 0 {
				prev := runes[i-1]
				nextLower := i+1 < len(runes) && unicode.IsLower(runes[i+1])
				if unicode.IsLower(prev) || unicode.IsDigit(prev) || (unicode.IsUpper(prev) && nextLower) {
					if b.Len() > 0 && !strings.HasSuffix(b.String(), "_") {
						b.WriteByte('_')
					}
				}
			}
			b.WriteRune(unicode.ToLower(r))
		} else {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func slugifyLabel(s string) string {
	var b strings.Builder
	lastUnder := false
	for _, r := range strings.ToLower(s) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
			lastUnder = false
		} else if !lastUnder && b.Len() > 0 {
			b.WriteByte('_')
			lastUnder = true
		}
	}
	return strings.TrimSuffix(b.String(), "_")
}
