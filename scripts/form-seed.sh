#!/usr/bin/env bash
# form-seed.sh — the brand's two feedback forms, stated through the typed form ops.
# Re-runnable: each form is a PUT on its slug (made when missing, replaced whole
# when held), read back from its public address, and checked field by field.
#
#   video-feedback       the hanzo.ai video ad's rating and comment (components/beta/PipAd.tsx)
#   enterprise-feedback  "Anything we should know?" on the Enterprise order's review and after contact sales
#
#   TOKEN=<bearer of an admin of the brand org> scripts/form-seed.sh
#
# ORG is the brand org the forms live in (default hanzo); their public ids are
# $ORG.video-feedback and $ORG.enterprise-feedback.
set -euo pipefail

API=${API:-https://api.hanzo.ai}
ORG=${ORG:-hanzo}
: "${TOKEN:?TOKEN must be a bearer of an admin of $ORG}"
command -v jq >/dev/null || { echo "jq is required" >&2; exit 2; }

# call METHOD PATH [BODY] — prints the body; fails on any status but 2xx.
call() {
  local method=$1 path=$2 body=${3:-} out code
  out=$(curl -sS -X "$method" "$API$path" -H "Authorization: Bearer $TOKEN" -H "X-Org-Id: $ORG" \
    -H 'Content-Type: application/json' ${body:+--data "$body"} -w $'\n%{http_code}')
  code=${out##*$'\n'}
  out=${out%$'\n'*}
  if [[ $code != 2* ]]; then
    echo "$method $path: HTTP $code: $out" >&2
    return 1
  fi
  printf '%s' "$out"
}

VIDEO='{
  "slug": "video-feedback", "kind": "form", "title": "How was the video?",
  "fields": [
    {"ref": "rating", "type": "rating", "title": "Rate the video", "steps": 5},
    {"ref": "comment", "type": "long_text", "title": "Feedback, questions, or ideas for our team", "validations": {"max_length": 2000}}
  ],
  "hidden": ["video", "source"],
  "thanks": {"title": "Thanks! Feedback received."},
  "settings": {"visibility": "public", "language": "en", "mode": "all", "progress": false},
  "tags": ["video", "beta"],
  "sinks": {"events": true, "crm": {}}
}'

ENTERPRISE='{
  "slug": "enterprise-feedback", "kind": "form", "title": "Anything we should know?",
  "fields": [
    {"ref": "note", "type": "long_text", "title": "Anything we should know?", "required": true, "validations": {"max_length": 4000}},
    {"ref": "email", "type": "email", "title": "Work email"}
  ],
  "hidden": ["step", "order", "plan", "seats"],
  "thanks": {"title": "Thanks. Sales will read this before they reply."},
  "settings": {"visibility": "public", "language": "en", "mode": "all", "progress": false},
  "tags": ["enterprise"],
  "sinks": {"events": true, "crm": {"email": "email", "list": "Enterprise"}}
}'

BETA_APPLY='{
  "slug": "beta-apply", "kind": "form", "title": "Hanzo Beta application",
  "fields": [
    {"ref": "builds", "type": "long_text", "title": "What do you build?"},
    {"ref": "first", "type": "long_text", "title": "What do you want to try first?"},
    {"ref": "agents", "type": "long_text", "title": "Which coding agents do you use today?"},
    {"ref": "cadence", "type": "short_text", "title": "How often do you use AI?"},
    {"ref": "spend", "type": "short_text", "title": "What do you spend on AI tools a month?"},
    {"ref": "blurb", "type": "long_text", "title": "Tell us about what you are working on"},
    {"ref": "company", "type": "short_text", "title": "Where do you work?"},
    {"ref": "role", "type": "short_text", "title": "What is your role?"},
    {"ref": "email", "type": "email", "title": "Email"},
    {"ref": "name", "type": "short_text", "title": "Name"}
  ],
  "hidden": ["program", "source"],
  "thanks": {"title": "Your application is in."},
  "settings": {"visibility": "public", "language": "en", "mode": "all", "progress": true},
  "tags": ["beta"],
  "sinks": {"events": true, "crm": {"email": "email", "name": "name", "company": "company", "list": "Beta"}}
}'

RESEARCH_APPLY='{
  "slug": "research-apply", "kind": "form", "title": "Hanzo Research Access application",
  "fields": [
    {"ref": "want", "type": "long_text", "title": "Which research do you want to evaluate?"},
    {"ref": "where", "type": "short_text", "title": "Where would you run it?"},
    {"ref": "who", "type": "short_text", "title": "Who are you evaluating for?"},
    {"ref": "publish", "type": "short_text", "title": "Will you publish what you find?"},
    {"ref": "task", "type": "long_text", "title": "What will you evaluate it on?"},
    {"ref": "blurb", "type": "long_text", "title": "Tell us about your research goals"},
    {"ref": "links", "type": "short_text", "title": "Anything we should read?"},
    {"ref": "company", "type": "short_text", "title": "Where do you work?"},
    {"ref": "role", "type": "short_text", "title": "What is your role?"},
    {"ref": "email", "type": "email", "title": "Email"},
    {"ref": "name", "type": "short_text", "title": "Name"}
  ],
  "hidden": ["program", "source"],
  "thanks": {"title": "Your research application is in."},
  "settings": {"visibility": "public", "language": "en", "mode": "all", "progress": true},
  "tags": ["beta", "research"],
  "sinks": {"events": true, "crm": {"email": "email", "name": "name", "company": "company", "list": "Research"}}
}'

PARTNER_APPLY='{
  "slug": "partner-apply", "kind": "form", "title": "Hanzo Design Partner application",
  "fields": [
    {"ref": "building", "type": "short_text", "title": "What are you building with Hanzo?"},
    {"ref": "size", "type": "short_text", "title": "How big is your team?"},
    {"ref": "when", "type": "short_text", "title": "When do you want it in production?"},
    {"ref": "budget", "type": "short_text", "title": "What is your monthly AI and compute budget?"},
    {"ref": "need", "type": "long_text", "title": "What do you need from us?"},
    {"ref": "blurb", "type": "long_text", "title": "Tell us about what you are working on"},
    {"ref": "company", "type": "short_text", "title": "Where do you work?"},
    {"ref": "role", "type": "short_text", "title": "What is your role?"},
    {"ref": "email", "type": "email", "title": "Email"},
    {"ref": "name", "type": "short_text", "title": "Name"}
  ],
  "hidden": ["program", "source"],
  "thanks": {"title": "Your design partner application is in."},
  "settings": {"visibility": "public", "language": "en", "mode": "all", "progress": true},
  "tags": ["beta", "partner"],
  "sinks": {"events": true, "crm": {"email": "email", "name": "name", "company": "company", "list": "Design partners"}}
}'

BETA_FEEDBACK='{
  "slug": "beta-feedback", "kind": "form", "title": "Tell the team",
  "fields": [
    {"ref": "kind", "type": "short_text", "title": "What you are sending"},
    {"ref": "note", "type": "long_text", "title": "Your feedback", "required": true, "validations": {"max_length": 4000}},
    {"ref": "email", "type": "email", "title": "Email"},
    {"ref": "name", "type": "short_text", "title": "Name"}
  ],
  "hidden": ["program", "source"],
  "thanks": {"title": "Sent. The team has it."},
  "settings": {"visibility": "public", "language": "en", "mode": "all", "progress": false},
  "tags": ["beta", "feedback"],
  "sinks": {"events": true, "crm": {"email": "email", "name": "name", "list": "Beta"}}
}'

ALPHA_APPLY='{
  "slug": "alpha-apply", "kind": "form", "title": "Hanzo Alpha application",
  "fields": [
    {"ref": "early_adopter", "type": "short_text", "title": "What have you been an early adopter of?"},
    {"ref": "alpha_build", "type": "long_text", "title": "What will you build first?"},
    {"ref": "why_alpha", "type": "long_text", "title": "Why you?"},
    {"ref": "email", "type": "email", "title": "Email"},
    {"ref": "name", "type": "short_text", "title": "Name"}
  ],
  "hidden": ["program", "source"],
  "thanks": {"title": "Alpha requested."},
  "settings": {"visibility": "public", "language": "en", "mode": "all", "progress": false},
  "tags": ["alpha", "beta"],
  "sinks": {"events": true, "crm": {"email": "email", "name": "name", "list": "Alpha"}}
}'

CONTACT_SALES='{
  "slug": "contact-sales", "kind": "form", "title": "Contact sales",
  "fields": [
    {"ref": "name", "type": "short_text", "title": "Name"},
    {"ref": "email", "type": "email", "title": "Work email", "required": true},
    {"ref": "company", "type": "short_text", "title": "Company"},
    {"ref": "role", "type": "short_text", "title": "Role"},
    {"ref": "size", "type": "short_text", "title": "Company size"},
    {"ref": "country", "type": "short_text", "title": "Country"},
    {"ref": "need", "type": "long_text", "title": "How can we help?"}
  ],
  "hidden": ["program", "source"],
  "thanks": {"title": "Thanks. Our team will be in touch shortly."},
  "settings": {"visibility": "public", "language": "en", "mode": "all", "progress": false},
  "tags": ["sales", "enterprise"],
  "sinks": {"events": true, "crm": {"email": "email", "name": "name", "company": "company", "list": "Enterprise"}}
}'

FOUNDER_APPLY='{
  "slug": "founder-apply", "kind": "form", "title": "Hanzo Founder Program claim",
  "fields": [
    {"ref": "email", "type": "email", "title": "Email of your Hanzo account", "required": true},
    {"ref": "name", "type": "short_text", "title": "Name"},
    {"ref": "github", "type": "short_text", "title": "GitHub username", "required": true},
    {"ref": "pull", "type": "short_text", "title": "Merged pull request", "required": true}
  ],
  "hidden": ["source"],
  "thanks": {"title": "Sent."},
  "settings": {"visibility": "public", "language": "en", "mode": "all", "progress": false},
  "tags": ["founder"],
  "sinks": {"events": true, "crm": {"email": "email", "name": "name", "list": "Founders"}}
}'

REVIEW_SUBMIT='{
  "slug": "review-submit", "kind": "form", "title": "Submit a Hanzo review",
  "fields": [
    {"ref": "name", "type": "short_text", "title": "Your name", "required": true},
    {"ref": "role", "type": "short_text", "title": "Role"},
    {"ref": "company", "type": "short_text", "title": "Company"},
    {"ref": "product", "type": "short_text", "title": "Product"},
    {"ref": "rating", "type": "rating", "title": "Rating", "steps": 5},
    {"ref": "title", "type": "short_text", "title": "Headline", "required": true},
    {"ref": "review", "type": "long_text", "title": "Review", "required": true}
  ],
  "hidden": ["source"],
  "thanks": {"title": "Thank you! Your review has been published on Hanzo Reviews."},
  "settings": {"visibility": "public", "language": "en", "mode": "all", "progress": false},
  "tags": ["reviews"],
  "sinks": {"events": true, "crm": {"name": "name", "company": "company", "list": "Reviews"}}
}'

PRODUCT_FEEDBACK='{
  "slug": "product-feedback", "kind": "form", "title": "Submit feedback",
  "fields": [
    {"ref": "rating", "type": "rating", "title": "How was your experience?", "steps": 5, "required": true},
    {"ref": "comment", "type": "long_text", "title": "Give us more details (optional)", "validations": {"max_length": 4000}},
    {"ref": "surface", "type": "short_text", "title": "Surface"},
    {"ref": "email", "type": "email", "title": "Email"},
    {"ref": "name", "type": "short_text", "title": "Name"}
  ],
  "hidden": ["program", "source", "page", "org"],
  "thanks": {"title": "Thank you for your feedback!"},
  "settings": {"visibility": "public", "language": "en", "mode": "all", "progress": false},
  "tags": ["feedback", "product"],
  "sinks": {"events": true, "crm": {"email": "email", "name": "name", "list": "Feedback"}}
}'

ARTICLE_FEEDBACK='{
  "slug": "article-feedback", "kind": "form", "title": "Did this answer your question?",
  "fields": [
    {"ref": "rating", "type": "rating", "title": "Did this answer your question?", "steps": 3, "required": true},
    {"ref": "article", "type": "short_text", "title": "Article", "required": true},
    {"ref": "comment", "type": "long_text", "title": "Additional feedback", "validations": {"max_length": 2000}},
    {"ref": "email", "type": "email", "title": "Email"}
  ],
  "hidden": ["program", "source", "language"],
  "thanks": {"title": "Thanks for letting us know!"},
  "settings": {"visibility": "public", "language": "en", "mode": "all", "progress": false},
  "tags": ["feedback", "help"],
  "sinks": {"events": true, "crm": {"email": "email", "list": "Feedback"}}
}'

HELP_CHAT='{
  "slug": "help-chat", "kind": "form", "title": "Help & Support conversation",
  "fields": [
    {"ref": "subject", "type": "short_text", "title": "Subject", "required": true},
    {"ref": "message", "type": "long_text", "title": "Message", "required": true, "validations": {"max_length": 4000}},
    {"ref": "transcript", "type": "long_text", "title": "Conversation transcript", "validations": {"max_length": 4000}},
    {"ref": "priority", "type": "short_text", "title": "Priority"},
    {"ref": "email", "type": "email", "title": "Email"},
    {"ref": "name", "type": "short_text", "title": "Name"}
  ],
  "hidden": ["program", "source", "page", "org", "ticket"],
  "thanks": {"title": "Your support message has been recorded."},
  "settings": {"visibility": "public", "language": "en", "mode": "all", "progress": false},
  "tags": ["support", "help"],
  "sinks": {"events": true, "crm": {"email": "email", "name": "name", "list": "Support"}}
}'

SALES_CHAT='{
  "slug": "sales-chat", "kind": "form", "title": "Buying Specialist conversation",
  "fields": [
    {"ref": "message", "type": "long_text", "title": "What are you looking to build or evaluate?", "required": true, "validations": {"max_length": 4000}},
    {"ref": "transcript", "type": "long_text", "title": "Conversation transcript", "validations": {"max_length": 4000}},
    {"ref": "plan", "type": "short_text", "title": "Plan of interest"},
    {"ref": "seats", "type": "short_text", "title": "Estimated seats"},
    {"ref": "email", "type": "email", "title": "Work email"},
    {"ref": "name", "type": "short_text", "title": "Name"},
    {"ref": "company", "type": "short_text", "title": "Company"}
  ],
  "hidden": ["program", "source", "page", "org"],
  "thanks": {"title": "Saved. Our sales team has your conversation."},
  "settings": {"visibility": "public", "language": "en", "mode": "all", "progress": false},
  "tags": ["sales", "chat", "enterprise"],
  "sinks": {"events": true, "crm": {"email": "email", "name": "name", "company": "company", "list": "Sales leads"}}
}'

STARTUPS_APPLY='{
  "slug": "startups-apply", "kind": "form", "title": "Hanzo Startup Program application",
  "fields": [
    {"ref": "company", "type": "short_text", "title": "Company name", "required": true, "validations": {"max_length": 200}},
    {"ref": "website", "type": "short_text", "title": "Website", "validations": {"max_length": 255}},
    {"ref": "contact_name", "type": "short_text", "title": "Your name", "required": true, "validations": {"max_length": 200}},
    {"ref": "email", "type": "email", "title": "Email", "required": true},
    {"ref": "role", "type": "short_text", "title": "Your role", "validations": {"max_length": 200}},
    {"ref": "stage", "type": "dropdown", "title": "Company stage", "required": true, "choices": [{"ref": "idea", "label": "Idea"}, {"ref": "pre-seed", "label": "Pre-seed"}, {"ref": "seed", "label": "Seed"}, {"ref": "series-a", "label": "Series A"}, {"ref": "series-b-plus", "label": "Series B+"}]},
    {"ref": "investors", "type": "short_text", "title": "Investors (funds, angels)", "validations": {"max_length": 255}},
    {"ref": "tier1_investors", "type": "multiple_choice", "multiple": true, "title": "Tier-1 funds", "choices": [{"ref": "a16z", "label": "Andreessen Horowitz (a16z)"}, {"ref": "sequoia", "label": "Sequoia"}, {"ref": "benchmark", "label": "Benchmark"}, {"ref": "greylock", "label": "Greylock"}, {"ref": "accel", "label": "Accel"}, {"ref": "founders-fund", "label": "Founders Fund"}, {"ref": "lightspeed", "label": "Lightspeed"}, {"ref": "index-ventures", "label": "Index Ventures"}, {"ref": "bessemer", "label": "Bessemer"}, {"ref": "khosla", "label": "Khosla Ventures"}, {"ref": "gv", "label": "GV (Google Ventures)"}, {"ref": "techstars", "label": "Techstars"}]},
    {"ref": "amount_raised", "type": "short_text", "title": "Amount raised", "validations": {"max_length": 100}},
    {"ref": "team_size", "type": "short_text", "title": "Team size", "validations": {"max_length": 100}},
    {"ref": "building", "type": "long_text", "title": "What are you building?", "required": true, "validations": {"max_length": 4000}},
    {"ref": "use_cases", "type": "multiple_choice", "multiple": true, "title": "What will you use Hanzo for?", "choices": [{"ref": "ai", "label": "AI & inference"}, {"ref": "deploy", "label": "Deploy"}, {"ref": "data", "label": "Data"}, {"ref": "compute", "label": "Compute"}]},
    {"ref": "infra_spend", "type": "short_text", "title": "Current infra / compute spend per month", "validations": {"max_length": 100}},
    {"ref": "byo_compute", "type": "yes_no", "title": "I want to bring my own compute"},
    {"ref": "byo_hardware", "type": "short_text", "title": "What hardware do you have?", "validations": {"max_length": 255}},
    {"ref": "techstars_batch", "type": "short_text", "title": "Techstars affiliation", "validations": {"max_length": 100}},
    {"ref": "heard_via", "type": "dropdown", "title": "How did you hear about us?", "choices": [{"ref": "search", "label": "Search"}, {"ref": "x", "label": "X / Twitter"}, {"ref": "linkedin", "label": "LinkedIn"}, {"ref": "friend", "label": "A friend or colleague"}, {"ref": "techstars-connection", "label": "A Techstars connection"}, {"ref": "investor", "label": "An investor"}, {"ref": "event", "label": "An event or conference"}, {"ref": "docs-github", "label": "Hanzo docs or GitHub"}, {"ref": "other", "label": "Other"}]}
  ],
  "hidden": ["source"],
  "thanks": {"title": "Application received. We will be in touch."},
  "settings": {"visibility": "public", "language": "en", "mode": "all", "progress": false, "identity": "verified"},
  "tags": ["startups", "sales"],
  "sinks": {"events": true, "crm": {"email": "email", "name": "contact_name", "company": "company", "list": "Startups"}}
}'

# state SLUG BODY — PUT the form on its slug, then read it back from its public
# address and check every field ref and hidden name came back.
state() {
  local slug=$1 body=$2 put public want got
  put=$(call PUT "/v1/form/forms/$slug" "$body")
  echo ">> $slug: $(jq -r '.public_id' <<<"$put") (id $(jq -r '.id' <<<"$put"))"
  public=$(call GET "/v1/form/public/$ORG.$slug")
  want=$(jq -c '[.fields[].ref, (.hidden // [])[]]' <<<"$body")
  got=$(jq -c '[.fields[].ref, (.hidden // [])[]]' <<<"$public")
  if [[ $want != "$got" ]]; then
    echo "$slug: the public form reads $got, want $want" >&2
    return 1
  fi
  echo "   public: $API/v1/form/public/$ORG.$slug  fields+hidden $got"
}

state video-feedback "$VIDEO"
state enterprise-feedback "$ENTERPRISE"
state beta-apply "$BETA_APPLY"
state research-apply "$RESEARCH_APPLY"
state partner-apply "$PARTNER_APPLY"
state beta-feedback "$BETA_FEEDBACK"
state alpha-apply "$ALPHA_APPLY"
state contact-sales "$CONTACT_SALES"
state founder-apply "$FOUNDER_APPLY"
state review-submit "$REVIEW_SUBMIT"
state product-feedback "$PRODUCT_FEEDBACK"
state article-feedback "$ARTICLE_FEEDBACK"
state help-chat "$HELP_CHAT"
state sales-chat "$SALES_CHAT"
state startups-apply "$STARTUPS_APPLY"
echo ">> seeded"
