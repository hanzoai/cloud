# Generated from typed ops. Do not edit.
# Every struct and method below is derived from one op's In and Out, and
# every offset from the layout the op-call plane encodes against.

package form

struct DeliveriesIn {
    ID       text @0
    Webhook  text @8
    Page     i64  @16
    PageSize i64  @24
}

struct Delivery {
    ID       text @0
    Webhook  text @8
    Response text @16
    Status   text @24
    Attempts i64  @32
    Code     i64  @40
    Error    text @48
    Next     text @56
    Created  text @64
    Updated  text @72
}

struct DeliveryList {
    Items    list<bytes> @0
    Page     i64         @8
    PageSize i64         @16
    Total    i64         @24
}

struct DeliveryRef {
    ID       text @0
    Webhook  text @8
    Delivery text @16
}

struct ExportIn {
    ResponsesIn bytes @0
    Format      text  @8
}

struct Form {
    ID       text        @0
    Slug     text        @8
    PublicID text        @16
    Kind     text        @24
    Title    text        @32
    Fields   list<bytes> @40
    Welcome  bytes       @48
    Thanks   bytes       @56
    Hidden   list<text>  @64
    Settings bytes       @72
    Theme    text        @80
    Tags     list<text>  @88
    Sinks    bytes       @96
    Created  text        @104
    Updated  text        @112
}

struct FormList {
    Items    list<bytes> @0
    Page     i64         @8
    PageSize i64         @16
    Total    i64         @24
}

struct FormPatch {
    ID       text        @0
    Kind     list<text>  @8
    Title    list<text>  @16
    Fields   list<bytes> @24
    Welcome  bytes       @32
    Thanks   bytes       @40
    Hidden   list<text>  @48
    Settings bytes       @56
    Theme    list<text>  @64
    Tags     list<text>  @72
    Sinks    bytes       @80
}

struct FormRef {
    ID text @0
}

struct FormSettings {
    Saved    bool  @0
    Settings bytes @8
}

struct FormsIn {
    Page     i64  @0
    PageSize i64  @8
    Search   text @16
    Tag      text @24
}

struct Imports {
    Created  i64         @0
    Existing i64         @8
    Failed   list<bytes> @16
}

struct PublicForm {
    ID       text        @0
    Kind     text        @8
    Title    text        @16
    Fields   list<bytes> @24
    Welcome  bytes       @32
    Thanks   bytes       @40
    Hidden   list<text>  @48
    Settings bytes       @56
    Theme    bytes       @64
}

struct PublicRef {
    ID text @0
}

struct Receipt {
    ID          text  @0
    Completed   bool  @8
    SubmittedAt text  @16
    Thanks      bytes @24
}

struct ResponseRef {
    ID       text @0
    Response text @8
}

struct ResponsesIn {
    ID                  text       @0
    Page                i64        @8
    PageSize            i64        @16
    Since               text       @24
    Until               text       @32
    Completed           list<bool> @40
    Query               text       @48
    Filed               list<bool> @56
    Sort                text       @64
    IncludedResponseIDs text       @72
    ExcludedResponseIDs text       @80
}

struct Settings {
    Types     list<bytes> @0
    Limits    bytes       @8
    Sinks     bytes       @16
    Retention i64         @24
}

struct Summary {
    Form           text        @0
    Views          list<i64>   @8
    Starts         i64         @16
    Completions    i64         @24
    CompletionRate f64         @32
    Scanned        i64         @40
    Truncated      bool        @48
    Fields         list<bytes> @56
}

struct SummaryIn {
    ID    text @0
    Since text @8
    Until text @16
}

struct Theme {
    ID         text  @0
    Name       text  @8
    Colors     bytes @16
    Font       text  @24
    Background text  @32
    Created    text  @40
    Updated    text  @48
}

struct ThemeList {
    Items list<bytes> @0
}

struct ThemeRef {
    ID text @0
}

struct Webhook {
    ID      text @0
    Form    text @8
    Tag     text @16
    URL     text @24
    Enabled bool @32
    Secret  text @40
    Created text @48
    Updated text @56
}

struct WebhookIn {
    ID      text       @0
    Webhook text       @8
    Tag     text       @16
    URL     text       @24
    Enabled list<bool> @32
    Secret  text       @40
    Rotate  bool       @48
}

struct WebhookList {
    Items list<bytes> @0
}

struct WebhookRef {
    ID      text @0
    Webhook text @8
}

interface form {
    # Makes a form in the caller's org and answers it as stored, its fields' ids
    # assigned and its defaults filled: kind form, visibility public, language en,
    # one question at a time, and the platform's default sinks when it names none.
    # A slug, when given, is the form's id for good: a second form with the same
    # slug is refused (409). Every problem with the form is named at once (400). An
    # org admin only.
    create_form(req: Form) returns (rep: Form)
    # Makes a theme in the caller's org: a name, four colours (#rrggbb), a font
    # family and an https background image, each optional but the name. An org
    # admin only.
    create_form_theme(req: Theme) returns (rep: Theme)
    # Adds a webhook to a form: every completed response is posted to its https
    # address as JSON, signed with Hanzo-Signature: t=<unix seconds>,sha256=<base64
    # of HMAC-SHA256(secret, "<seconds>." + body)>; a receiver refuses a timestamp
    # older than five minutes and an event_id it has seen. The secret is the one
    # given (16 to 256 characters) or one minted, kept in KMS and answered here
    # once. A form has at
    # most the platform's number of webhooks. An org admin only.
    create_form_webhook(req: WebhookIn) returns (rep: Webhook)
    # Deletes a form with every response, webhook and delivery it has, and the
    # webhooks' signing secrets. An org admin only.
    delete_form(req: FormRef)
    # Deletes one response of a form, and its webhook deliveries. What the sinks
    # already hold (the CRM's timeline, a webhook's receiver) is theirs. An org admin
    # only.
    delete_form_response(req: ResponseRef)
    # Deletes a theme. A form drawn in it is refused (409) until it names another,
    # so no form is left naming a theme that is gone. An org admin only.
    delete_form_theme(req: ThemeRef)
    # Deletes a webhook, its deliveries and its secret. An org admin only.
    delete_form_webhook(req: WebhookRef)
    # Reads one form of the caller's org, by its id or its slug. An org admin only.
    get_form(req: FormRef) returns (rep: Form)
    # Reads the platform's settings of forms: every field type with whether forms may
    # ask it and its bound, the limits every form and submission is held to (fields,
    # hidden values, body bytes, submissions per address per window, webhooks), the
    # sinks a new form takes when it names none, and the days a response is kept.
    # `saved` is false when nothing is stored and the defaults are in force.
    # SuperAdmin only. 503 when the settings store does not answer.
    get_form_settings() returns (rep: FormSettings)
    # Reads one theme of the caller's org. An org admin only.
    get_form_theme(req: ThemeRef) returns (rep: Theme)
    # Reads one webhook of a form. Its secret is never read back. An org admin only.
    get_form_webhook(req: WebhookRef) returns (rep: Webhook)
    # Reads a published form as its page draws it: the questions, the screens, the
    # hidden values it takes, how it is answered and its theme. Nothing about where
    # its responses go. No credential is needed; a form that is private, deleted or
    # never was is a 404.
    get_public_form(req: PublicRef) returns (rep: PublicForm)
    # Lists a webhook's deliveries, newest first: each response posted, how many
    # attempts it took, the last status the address answered and why it failed. An
    # org admin only.
    list_form_deliveries(req: DeliveriesIn) returns (rep: DeliveryList)
    # Lists the caller org's themes by name. An org admin only.
    list_form_themes() returns (rep: ThemeList)
    # Lists a form's webhooks. Secrets are never read back. An org admin only.
    list_form_webhooks(req: FormRef) returns (rep: WebhookList)
    # Lists the caller org's forms, newest first, a page at a time; search keeps
    # the forms whose title holds it and tag the ones carrying it. An org admin only.
    list_forms(req: FormsIn) returns (rep: FormList)
    # Posts a delivery's response to its webhook again, now, and answers how it
    # went. A delivery that failed, or was waiting on a retry when the service
    # restarted, is sent this way. An org admin only.
    redeliver_form_webhook(req: DeliveryRef) returns (rep: Delivery)
    # Replaces a form whole and answers it as stored. Named by a slug no form has
    # yet, it makes the form under that slug, so a definition written twice is one
    # form: the way a seed states a form. A field sent with its id keeps it; a type
    # the platform no longer offers stays answerable where the form already asks
    # it, and is refused where it is new. An org admin only.
    replace_form(req: Form) returns (rep: Form)
    # Replaces a theme whole; every form drawn in it is drawn in the new one. An
    # org admin only.
    replace_form_theme(req: Theme) returns (rep: Theme)
    # Replaces a webhook's tag, address and whether it is enabled. A secret given is
    # kept in place of the one held, rotate mints a new one, and either is answered
    # once; otherwise the secret held stays. An org admin only.
    replace_form_webhook(req: WebhookIn) returns (rep: Webhook)
    # Replaces the platform's settings of forms whole and answers them as stored.
    # Every field type is listed once, enabled or not, each bound inside what the
    # store holds; every limit is inside its range; the default sinks name no field.
    # A type turned off stays answerable on the forms that already ask it, and is
    # refused on a form that adds it. The forms service reads the change within 30
    # seconds.
    # SuperAdmin only. 400 names every problem at once; 503 when the settings store
    # does not take the change.
    # A limit the document leaves out, forward and welcome among them, is the
    # default.
    set_form_settings(req: Settings) returns (rep: FormSettings)
    # Sums up a form's responses: how many were begun and completed, and for each
    # field how many answered it, the distribution of what was said (choices,
    # points, yes and no), the mean, least and most of a number, and how many open
    # responses stopped in front of it. since and until narrow it to when the
    # responses were made. At most the newest 50000 are read, and truncated says so.
    # An org admin only.
    summarize_form(req: SummaryIn) returns (rep: Summary)
    # Changes the parts of a form the body names and keeps the rest, then answers it
    # as stored. The whole form is checked as it will be, exactly as a replace is.
    # An org admin only.
    update_form(req: FormPatch) returns (rep: Form)
}

# ---------------------------------------------------------------------
# 23 op(s) here. What follows is what this schema does not carry.
#
# blocked (6) — the op is absent; the field has no wire form:
#   export_form_responses  Export.Items  []form.Response  (no wire form)
#   file_form_response  Response.Hidden  map[string]string  (map)
#   get_form_response  Response  form.Response  (reaches one)
#   import_form_responses  ImportIn.Responses  []form.Imported  (no wire form)
#   list_form_responses  ResponseList.Items  []form.Response  (no wire form)
#   submit_form_response  Submission.Hidden  map[string]string  (map)
#
# opaque (28) — crosses, arrives without its name:
#   DeliveryList.Items  form.Delivery (list element)
#   ExportIn.ResponsesIn  form.ResponsesIn
#   Form.Fields  form.Field (list element)
#   Form.Settings  form.Options
#   Form.Sinks  form.Sinks
#   Form.Thanks  form.Screen
#   Form.Welcome  form.Screen
#   FormList.Items  form.Form (list element)
#   FormPatch.Fields  form.Field (list element)
#   FormPatch.Settings  form.Options
#   FormPatch.Sinks  form.Sinks
#   FormPatch.Thanks  form.Screen
#   FormPatch.Welcome  form.Screen
#   FormSettings.Settings  form.Settings
#   Imports.Failed  form.ImportFailure (list element)
#   PublicForm.Fields  form.Field (list element)
#   PublicForm.Settings  form.Drawn
#   PublicForm.Thanks  form.Screen
#   PublicForm.Theme  form.Theme
#   PublicForm.Welcome  form.Screen
#   Receipt.Thanks  form.Screen
#   Settings.Limits  form.Limits
#   Settings.Sinks  form.Sinks
#   Settings.Types  form.TypeRule (list element)
#   Summary.Fields  form.FieldSummary (list element)
#   Theme.Colors  form.Colors
#   ThemeList.Items  form.Theme (list element)
#   WebhookList.Items  form.Webhook (list element)
