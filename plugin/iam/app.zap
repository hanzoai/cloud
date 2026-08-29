# Generated from typed ops. Do not edit.
# Every struct and method below is derived from one op's In and Out, and
# every offset from the layout the op-call plane encodes against.

package iam

struct ApplicationQuery {
    Owner text @0
}

struct ApplicationRef {
    Owner text @0
    Name  text @8
}

struct AuditLog {
    Model        bytes @0
    Owner        text  @8
    Name         text  @16
    CreatedTime  text  @24
    Organization text  @32
    ClientIp     text  @40
    User         text  @48
    Method       text  @56
    RequestUri   text  @64
    Action       text  @72
    Language     text  @80
    Object       text  @88
    Response     text  @96
    StatusCode   i64   @104
    IsTriggered  bool  @112
}

struct Cert {
    Model            bytes @0
    Owner            text  @8
    Name             text  @16
    CreatedTime      text  @24
    DisplayName      text  @32
    Scope            text  @40
    Type             text  @48
    CryptoAlgorithm  text  @56
    BitSize          i64   @64
    ExpireInYears    i64   @72
    ExpireTime       text  @80
    DomainExpireTime text  @88
    Provider         text  @96
    Account          text  @104
    AccessKey        text  @112
    AccessSecret     text  @120
    Certificate      text  @128
    PrivateKey       text  @136
}

struct CreateOrganizationInput {
    Organization bytes @0
}

struct CreateSessionIn {
    Owner           text       @0
    Name            text       @8
    Application     text       @16
    SessionId       list<text> @24
    ExclusiveSignin bool       @32
}

struct DeleteOrganizationInput {
    Owner text @0
    Name  text @8
}

struct DeleteOrganizationOutput {
    Affected bool @0
}

struct DeleteOutput {
    Deleted bool @0
}

struct DeleteResponse {
    Deleted bool @0
}

struct DeleteResult {
    Deleted bool @0
}

struct DeleteSessionOut {
    Deleted bool @0
}

struct GetOrganizationInput {
    Owner text @0
    Name  text @8
}

struct Input {
    Owner        text @0
    Name         text @8
    CreatedTime  text @16
    Organization text @24
    ClientIp     text @32
    User         text @40
    Method       text @48
    RequestUri   text @56
    Action       text @64
    Language     text @72
    Object       text @80
    Response     text @88
    StatusCode   i64  @96
    IsTriggered  bool @104
}

struct Invitation {
    Model       bytes @0
    Owner       text  @8
    Name        text  @16
    CreatedTime text  @24
    UpdatedTime text  @32
    DisplayName text  @40
    Code        text  @48
    IsRegexp    bool  @56
    Quota       i64   @64
    UsedCount   i64   @72
    Application text  @80
    Username    text  @88
    Email       text  @96
    Phone       text  @104
    SignupGroup text  @112
    DefaultCode text  @120
    State       text  @128
}

struct Key {
    Model              bytes @0
    Owner              text  @8
    Name               text  @16
    CreatedTime        text  @24
    UpdatedTime        text  @32
    DisplayName        text  @40
    Type               text  @48
    Organization       text  @56
    Application        text  @64
    User               text  @72
    AccessKey          text  @80
    AccessSecret       text  @88
    AccessSecretDigest text  @96
    ExpireTime         text  @104
    State              text  @112
    Scope              text  @120
    Act                bool  @128
}

struct ListInput {
    Owner text @0
}

struct ListOrganizationsInput {
    Query     text @0
    Limit     i64  @8
    Cursor    text @16
    Forwarded text @24
}

struct ListOrganizationsOutput {
    Organizations list<bytes> @0
    Cursor        text        @8
}

struct ListOutput {
    AuditLogs list<bytes> @0
    Total     i64         @8
}

struct ListRequest {
    Owner text @0
}

struct ListResponse {
    Keys list<bytes> @0
}

struct ListSessionsIn {
    Owner       text @0
    Name        text @8
    Application text @16
}

struct ListSessionsOut {
    Sessions list<bytes> @0
}

struct Lookup {
    Owner text @0
    Name  text @8
    Email text @16
}

struct Organization {
    Model                  bytes       @0
    Owner                  text        @8
    Name                   text        @16
    CreatedTime            text        @24
    DisplayName            text        @32
    WebsiteUrl             text        @40
    Logo                   text        @48
    LogoDark               text        @56
    Favicon                text        @64
    Avatar                 text        @72
    Emoji                  text        @80
    HasPrivilegeConsent    bool        @88
    PasswordType           text        @96
    PasswordSalt           text        @104
    PasswordOptions        list<text>  @112
    PasswordObfuscatorType text        @120
    PasswordObfuscatorKey  text        @128
    PasswordExpireDays     i64         @136
    CountryCodes           list<text>  @144
    DefaultAvatar          text        @152
    UsePermanentAvatar     bool        @160
    DefaultApplication     text        @168
    UserTypes              list<text>  @176
    Tags                   list<text>  @184
    Languages              list<text>  @192
    ThemeData              bytes       @200
    MasterPassword         text        @208
    DefaultPassword        text        @216
    MasterVerificationCode text        @224
    IpWhitelist            text        @232
    InitScore              i64         @240
    EnableSoftDeletion     bool        @248
    IsProfilePublic        bool        @249
    UseEmailAsUsername     bool        @250
    EnableTour             bool        @251
    DisableSignin          bool        @252
    IpRestriction          text        @256
    NavItems               list<text>  @264
    UserNavItems           list<text>  @272
    WidgetItems            list<text>  @280
    MfaItems               list<bytes> @288
    MfaRememberInHours     i64         @296
    AccountMenu            text        @304
    AccountItems           list<bytes> @312
    FailedSigninLimit      i64         @320
    FailedSigninFrozenTime i64         @328
    DcrPolicy              text        @336
    LdapAttributes         list<text>  @344
    KerberosRealm          text        @352
    KerberosKdcHost        text        @360
    KerberosKeytab         text        @368
    KerberosServiceName    text        @376
    OrgBalance             f64         @384
    UserBalance            f64         @392
    BalanceCredit          f64         @400
    BalanceCurrency        text        @408
    IsPersonal             bool        @416
    Founder                text        @424
}

struct Permission {
    Model        bytes      @0
    Owner        text       @8
    Name         text       @16
    CreatedTime  text       @24
    DisplayName  text       @32
    Description  text       @40
    Users        list<text> @48
    Teams        list<text> @56
    Roles        list<text> @64
    Domains      list<text> @72
    AuthzModel   text       @80
    Adapter      text       @88
    ResourceType text       @96
    Resources    list<text> @104
    Actions      list<text> @112
    Effect       text       @120
    IsEnabled    bool       @128
    Submitter    text       @136
    Approver     text       @144
    ApproveTime  text       @152
    State        text       @160
}

struct Project {
    Model        bytes      @0
    Owner        text       @8
    Name         text       @16
    CreatedTime  text       @24
    DisplayName  text       @32
    Description  text       @40
    Organization text       @48
    Workspace    text       @56
    Tags         list<text> @64
    Tags_        text       @72
    Metadata     text       @80
    IsDefault    bool       @88
}

struct Ref {
    Owner text @0
    Name  text @8
}

struct Role {
    Model       bytes      @0
    Owner       text       @8
    Name        text       @16
    CreatedTime text       @24
    DisplayName text       @32
    Description text       @40
    Users       list<text> @48
    Users_      text       @56
    Teams       list<text> @64
    Teams_      text       @72
    Roles       list<text> @80
    Roles_      text       @88
    Domains     list<text> @96
    Domains_    text       @104
    IsEnabled   bool       @112
}

struct Session {
    Model           bytes      @0
    Owner           text       @8
    Name            text       @16
    Application     text       @24
    CreatedTime     text       @32
    SessionId       list<text> @40
    SessionId_      text       @48
    ExclusiveSignin bool       @56
}

struct SessionRef {
    Owner       text @0
    Name        text @8
    Application text @16
}

struct SetAvatarInput {
    Owner  text @0
    Name   text @8
    Avatar text @16
    Emoji  text @24
}

struct SetProfileInput {
    Owner       text @0
    Name        text @8
    DisplayName text @16
    WebsiteUrl  text @24
    Favicon     text @32
}

struct Team {
    Model        bytes      @0
    Owner        text       @8
    Name         text       @16
    CreatedTime  text       @24
    DisplayName  text       @32
    Description  text       @40
    Organization text       @48
    Parent       text       @56
    Users        list<text> @64
    Users_       text       @72
    IsEnabled    bool       @80
}

struct Token {
    Model               bytes @0
    Owner               text  @8
    Name                text  @16
    CreatedTime         text  @24
    Application         text  @32
    Organization        text  @40
    User                text  @48
    Code                text  @56
    UserCode            text  @64
    AccessToken         text  @72
    RefreshToken        text  @80
    AccessTokenHash     text  @88
    RefreshTokenHash    text  @96
    ExpiresIn           i64   @104
    Scope               text  @112
    TokenType           text  @120
    CodeChallenge       text  @128
    CodeChallengeMethod text  @136
    CodeIsUsed          bool  @144
    CodeExpireIn        i64   @152
    Resource            text  @160
    RedirectUri         text  @168
    Nonce               text  @176
    RefreshFamily       text  @184
    RefreshConsumed     bool  @192
    RefreshExpireIn     i64   @200
    PublicGrant         bool  @208
}

struct UpdateOrganizationInput {
    Organization bytes @0
}

struct UpdateSessionIn {
    Owner       text       @0
    Name        text       @8
    Application text       @16
    SessionId   list<text> @24
}

struct WebauthnCredential {
    Model             bytes      @0
    Owner             text       @8
    Name              text       @16
    CreatedTime       text       @24
    User              text       @32
    CredentialId      bytes      @40
    PublicKey         bytes      @48
    AttestationType   text       @56
    AttestationFormat text       @64
    Transport         list<text> @72
    Transport_        text       @80
    UserPresent       bool       @88
    UserVerified      bool       @89
    BackupEligible    bool       @90
    BackupState       bool       @91
    Aaguid            bytes      @96
    SignCount         u32        @104
    CloneWarning      bool       @108
    Attachment        text       @112
}

struct Workspace {
    Model        bytes      @0
    Owner        text       @8
    Name         text       @16
    CreatedTime  text       @24
    DisplayName  text       @32
    Description  text       @40
    Organization text       @48
    Bucket       text       @56
    Tags         list<text> @64
    Tags_        text       @72
    Metadata     text       @80
    IsDefault    bool       @88
}

struct accountBody {
    DisplayName text @0
    Avatar      text @8
    Bio         text @16
    Homepage    text @24
    Cookie      text @32
    Auth        text @40
}

struct assumeBody {
    Org       text @0
    Auth      text @8
    Forwarded text @16
}

struct certs_DeleteOutput {
    Deleted bool @0
}

struct certs_ListInput {
    Owner text @0
}

struct certs_ListOutput {
    Certs list<bytes> @0
    Total i64         @8
}

struct certs_Ref {
    Owner text @0
    Name  text @8
}

struct config {
    Schemes  list<bytes> @0
    Bulk     bytes       @8
    Password bytes       @16
    Docs     text        @24
    Etag     bytes       @32
    Filter   bytes       @40
    Patch    bytes       @48
    Schemas  list<text>  @56
    Sort     bytes       @64
}

struct invitations_DeleteOutput {
    Deleted bool @0
}

struct invitations_Input {
    Owner       text @0
    Name        text @8
    CreatedTime text @16
    UpdatedTime text @24
    DisplayName text @32
    Code        text @40
    IsRegexp    bool @48
    Quota       i64  @56
    UsedCount   i64  @64
    Application text @72
    Username    text @80
    Email       text @88
    Phone       text @96
    SignupGroup text @104
    DefaultCode text @112
    State       text @120
}

struct invitations_ListInput {
    Owner text @0
}

struct invitations_ListOutput {
    Invitations list<bytes> @0
    Total       i64         @8
}

struct invitations_Ref {
    Owner text @0
    Name  text @8
}

struct keys_Ref {
    Owner text @0
    Name  text @8
}

struct kind {
    Kind text @0
}

struct listProvidersIn {
    Owner text @0
}

struct listTokensIn {
    Owner        text @0
    Organization text @8
}

struct listTokensOut {
    Tokens list<bytes> @0
}

struct listWebauthnCredentialsIn {
    User text @0
}

struct listWebauthnCredentialsOut {
    WebauthnCredentials list<bytes> @0
}

struct lookup {
    User text @0
    Org  text @8
}

struct offer {
    ClientId text @0
}

struct passwordBody {
    Organization text @0
    Username     text @8
    Code         text @16
    OldPassword  text @24
    Password     text @32
    Cookie       text @40
    Auth         text @48
}

struct permission_DeleteResponse {
    Deleted bool @0
}

struct permission_ListRequest {
    Owner text @0
}

struct permission_ListResponse {
    Permissions list<bytes> @0
}

struct permission_Ref {
    Owner text @0
    Name  text @8
}

struct person {
    Owner        text @0
    Name         text @8
    DisplayName  text @16
    Email        text @24
    Phone        text @32
    Password     text @40
    PasswordType text @48
    IsAdmin      bool @56
    Auth         text @64
}

struct projects_DeleteOutput {
    Deleted bool @0
}

struct projects_Input {
    Owner        text       @0
    Name         text       @8
    CreatedTime  text       @16
    DisplayName  text       @24
    Description  text       @32
    Organization text       @40
    Workspace    text       @48
    Tags         list<text> @56
    Metadata     text       @64
    IsDefault    bool       @72
}

struct projects_ListInput {
    Owner text @0
}

struct projects_ListOutput {
    Projects list<bytes> @0
    Total    i64         @8
}

struct projects_Ref {
    Owner text @0
    Name  text @8
}

struct providerKey {
    Owner text @0
    Name  text @8
}

struct query {
    Organization text @0
    P            i64  @8
    Size         i64  @16
}

struct registration {
    Organization         text       @0
    Name                 text       @8
    ClientId             text       @16
    ClientSecret         text       @24
    GrantTypes           list<text> @32
    RedirectUris         list<text> @40
    DisplayName          text       @48
    Cert                 text       @56
    Public               bool       @64
    IsShared             bool       @65
    ExpireInHours        f64        @72
    RefreshExpireInHours f64        @80
    EnableCodeSignin     bool       @88
    Auth                 text       @96
}

struct roles_DeleteOutput {
    Deleted bool @0
}

struct roles_Input {
    Owner       text       @0
    Name        text       @8
    CreatedTime text       @16
    DisplayName text       @24
    Description text       @32
    Users       list<text> @40
    Teams       list<text> @48
    Roles       list<text> @56
    Domains     list<text> @64
    IsEnabled   bool       @72
}

struct roles_ListInput {
    Owner text @0
}

struct roles_ListOutput {
    Roles list<bytes> @0
    Total i64         @8
}

struct roles_Ref {
    Owner text @0
    Name  text @8
}

struct screen {
    ClientId     text @0
    ResponseType text @8
}

struct teams_DeleteOutput {
    Deleted bool @0
}

struct teams_Input {
    Name         text       @0
    CreatedTime  text       @8
    DisplayName  text       @16
    Description  text       @24
    Organization text       @32
    Parent       text       @40
    Users        list<text> @48
    IsEnabled    bool       @56
}

struct teams_ListOutput {
    Teams list<bytes> @0
    Total i64         @8
}

struct teams_Ref {
    Name text @0
}

struct tokenKey {
    Owner text @0
    Name  text @8
}

struct tokenMutation {
    Affected bool  @0
    Token    bytes @8
}

struct tokenResult {
    Token bytes @0
}

struct urn {
    Id text @0
}

struct users_DeleteOutput {
    Deleted bool @0
}

struct users_ListInput {
    Owner  text @0
    Email  text @8
    Limit  i64  @16
    Offset i64  @24
}

struct users_Ref {
    Owner text @0
    Name  text @8
}

struct webauthnCredentialKey {
    Owner text @0
    Name  text @8
}

struct webauthnCredentialMutationResult {
    Affected           bool  @0
    WebauthnCredential bytes @8
}

struct webauthnCredentialResult {
    WebauthnCredential bytes @0
}

struct workspaces_DeleteOutput {
    Deleted bool @0
}

struct workspaces_Input {
    Owner        text       @0
    Name         text       @8
    CreatedTime  text       @16
    DisplayName  text       @24
    Description  text       @32
    Organization text       @40
    Bucket       text       @48
    Tags         list<text> @56
    Metadata     text       @64
    IsDefault    bool       @72
}

struct workspaces_ListInput {
    Owner text @0
}

struct workspaces_ListOutput {
    Workspaces list<bytes> @0
    Total      i64         @8
}

struct workspaces_Ref {
    Owner text @0
    Name  text @8
}

interface iam {
    # Records an access token — the credential an application or integration
    # presents on a caller's behalf.
    addToken(req: Token) returns (rep: tokenResult)
    # Registers a passkey or security key for a person, so they
    # can sign in with their device instead of a password.
    addWebauthnCredential(req: WebauthnCredential) returns (rep: webauthnCredentialResult)
    # Makes a new organization — the account your users, applications, roles,
    # projects and workspaces are all named inside. It is the first write in a new
    # tenant, and a name already in use is refused rather than taken over.
    createOrganization(req: CreateOrganizationInput) returns (rep: Organization)
    # Records a sign-in and answers with the cookie id it minted. Signing in
    # again from another browser adds to the session rather than replacing it, so one
    # person can be signed in from a laptop and a phone at once.
    # Ask for an exclusive sign-in and the opposite holds: the new sign-in is the only
    # one left and every other browser is signed out. That is the setting to use when
    # one person may hold only one live session at a time.
    createSession(req: CreateSessionIn) returns (rep: Session)
    # Removes an organization and everything named inside it. There is no
    # undo, and every session issued under it stops working.
    # The built-in admin organization cannot be deleted — losing it would leave the
    # account with no way back in.
    deleteOrganization(req: DeleteOrganizationInput) returns (rep: DeleteOrganizationOutput)
    # Signs a person out of one application — the session ends and every
    # browser carrying it stops being authenticated.
    # A session that is already gone reports that nothing was deleted rather than an
    # error, so the call is safe to repeat.
    deleteSession(req: SessionRef) returns (rep: DeleteSessionOut)
    # Revokes an access token. Whatever was using it stops being
    # authorized at once.
    # A token that is already gone answers "nothing changed" rather than an error, so
    # the call is safe to repeat.
    deleteToken(req: tokenKey) returns (rep: tokenMutation)
    # Removes a passkey or security key — what you call when
    # a device is lost. Make sure the person has another way to sign in first.
    # A credential that is already gone answers "nothing changed" rather than an
    # error, so the call is safe to repeat.
    deleteWebauthnCredential(req: webauthnCredentialKey) returns (rep: webauthnCredentialMutationResult)
    # Removes an application. Anyone mid-sign-in through it is
    # turned away and its client credentials stop working, so retire the integration
    # before deleting it.
    delete_iam_applications_by_owner_by_name(req: ApplicationRef) returns (rep: DeleteResult)
    # Removes an audit entry. Retention policy is normally what should expire
    # a trail; deleting by hand leaves a gap a reviewer will notice.
    delete_iam_audit_logs_by_owner_by_name(req: Ref) returns (rep: DeleteOutput)
    # Removes a signing certificate. Tokens signed with it can no longer be
    # verified, so retire it only once nothing is still presenting them.
    delete_iam_certs_by_owner_by_name(req: certs_Ref) returns (rep: certs_DeleteOutput)
    # Withdraws an invitation. It stops being redeemable at once; anyone who
    # already joined through it keeps their account.
    delete_iam_invitations_by_owner_by_name(req: invitations_Ref) returns (rep: invitations_DeleteOutput)
    # Revokes an API key. Anything still presenting it stops being authorized at
    # once, so roll the replacement out before you revoke.
    delete_iam_keys_by_owner_by_name(req: keys_Ref) returns (rep: DeleteResponse)
    # Revokes a permission. Everyone who held access only through it loses
    # that access immediately; grants they hold by another route are untouched.
    delete_iam_permissions_by_owner_by_name(req: permission_Ref) returns (rep: permission_DeleteResponse)
    # Removes a project. The people and roles in your organization are
    # unchanged; what goes is the scope itself, so move anything addressed by it
    # first.
    delete_iam_projects_by_owner_by_name(req: projects_Ref) returns (rep: projects_DeleteOutput)
    # Removes a role. Everyone in it loses the access it carried; their
    # accounts, and any other role they hold, are untouched.
    delete_iam_roles_by_owner_by_name(req: roles_Ref) returns (rep: roles_DeleteOutput)
    # Removes a team. Everyone in it loses the access it carried; their
    # accounts, and any other team they are in, are untouched.
    delete_iam_teams_by_name(req: teams_Ref) returns (rep: teams_DeleteOutput)
    # Removes a person from your organization. Their sessions stop working
    # immediately and the account is gone rather than suspended — to keep the record
    # and only stop sign-in, update the user instead.
    delete_iam_users_by_owner_by_name(req: users_Ref) returns (rep: users_DeleteOutput)
    # Removes a workspace. The people and roles in your organization are
    # unchanged; what goes is the scope itself.
    delete_iam_workspaces_by_owner_by_name(req: workspaces_Ref) returns (rep: workspaces_DeleteOutput)
    # Returns one organization: its display, its defaults and the sign-in rules
    # everyone in it inherits.
    getOrganization(req: GetOrganizationInput) returns (rep: Organization)
    # Returns one person's session in one application — when it began and which
    # browsers or devices are still carrying it.
    getSession(req: SessionRef) returns (rep: Session)
    # Returns one access token: who and what it was issued to, and when it
    # expires.
    getToken(req: tokenKey) returns (rep: tokenResult)
    # Returns one passkey or security key: whose it is, what
    # device it lives on, and when it was registered.
    getWebauthnCredential(req: webauthnCredentialKey) returns (rep: webauthnCredentialResult)
    # Returns your organization's audit trail, newest first — who did
    # what, when, and from where. It is the record you reach for during a security
    # review or an incident.
    # You see your own organization's audit trail and no one else's; which organization that
    # is comes from your credentials, not from the request.
    get_iam_audit_logs(req: ListInput) returns (rep: ListOutput)
    # Returns one audit entry in full: the action, the person or key behind it,
    # and the request it came in on.
    get_iam_audit_logs_by_owner_by_name(req: Ref) returns (rep: AuditLog)
    # Returns your organization's signing certificates, newest first — the keys
    # the tokens your applications verify are signed with. Private key material is
    # masked.
    # You see your own organization's certificates and no one else's; which
    # organization that is comes from your credentials, not from the request, so a
    # query parameter can never widen the listing.
    get_iam_certs(req: certs_ListInput) returns (rep: certs_ListOutput)
    # Returns one signing certificate — its algorithm, its validity window and
    # its public half. The private key is masked.
    get_iam_certs_by_owner_by_name(req: certs_Ref) returns (rep: Cert)
    # Returns your organization's invitations, newest first — who has
    # been asked to join, on what terms, and how many seats each invitation still
    # has left.
    # You see your own organization's invitations and no one else's; which organization that
    # is comes from your credentials, not from the request.
    get_iam_invitations(req: invitations_ListInput) returns (rep: invitations_ListOutput)
    # Returns one invitation: who it is for, what it grants on acceptance, and
    # when it expires.
    get_iam_invitations_by_owner_by_name(req: invitations_Ref) returns (rep: Invitation)
    # Returns an organization's API keys, newest first — what each is called,
    # what it may reach, and its publishable half. Secret halves are never listed.
    # Which organization comes from your credentials, not from the request: you read
    # your own and no one else's. The capability that admits a confidential client to
    # this collection does not itself name a tenant, so the tenant is decided here.
    get_iam_keys(req: ListRequest) returns (rep: ListResponse)
    # Returns one API key: what it is called, what it may reach, and when it was
    # issued.
    get_iam_keys_by_owner_by_name(req: keys_Ref) returns (rep: Key)
    # Returns the permissions in one organization, newest first — each one a
    # grant saying which people or roles may do what, and to which resources.
    get_iam_permissions(req: permission_ListRequest) returns (rep: permission_ListResponse)
    # Returns one permission: who it grants to, what it allows, and the
    # resources it covers.
    get_iam_permissions_by_owner_by_name(req: permission_Ref) returns (rep: Permission)
    # Returns your organization's projects, newest first — the scope
    # people pick between when their work is separated by product or client rather
    # than by team.
    # You see your own organization's projects and no one else's; which organization that
    # is comes from your credentials, not from the request.
    get_iam_projects(req: projects_ListInput) returns (rep: projects_ListOutput)
    # Returns one project: what it is called and how it is set up.
    get_iam_projects_by_owner_by_name(req: projects_Ref) returns (rep: Project)
    # Returns your organization's roles, newest first — each a named group of
    # people that permissions are granted to.
    # You see your own organization's roles and no one else's; which organization
    # that is comes from your credentials, not from the request.
    get_iam_roles(req: roles_ListInput) returns (rep: roles_ListOutput)
    # Returns one role: who is in it, and the roles it includes.
    get_iam_roles_by_owner_by_name(req: roles_Ref) returns (rep: Role)
    # Returns one provisionable record kind in full.
    get_iam_scim_v2_resourcetypes_by_name(req: kind)
    # Returns one attribute definition in full.
    get_iam_scim_v2_schemas_by_id(req: urn)
    # Tells your identity provider which parts of SCIM this
    # directory supports, so it configures itself instead of you filling in a form.
    # Filtering and partial updates are supported. Bulk operations, sorting and
    # entity tags are not — an IdP that reads this will not attempt them.
    get_iam_scim_v2_serviceproviderconfig() returns (rep: config)
    # Returns your organization's teams, newest first — each a named set of
    # people that roles and permissions are granted to.
    # You see your own organization's teams and no one else's; which organization
    # that is comes from your credentials, not from the request.
    get_iam_teams() returns (rep: teams_ListOutput)
    # Returns one team: who is in it.
    get_iam_teams_by_name(req: teams_Ref) returns (rep: Team)
    # Returns your organization's workspaces, newest first — the scope a
    # team works in, alongside projects rather than instead of them.
    # You see your own organization's workspaces and no one else's; which organization that
    # is comes from your credentials, not from the request.
    get_iam_workspaces(req: workspaces_ListInput) returns (rep: workspaces_ListOutput)
    # Returns one workspace: what it is called and how it is set up.
    get_iam_workspaces_by_owner_by_name(req: workspaces_Ref) returns (rep: Workspace)
    # Returns the organizations you can act in, the ones you belong to first
    # and the rest after, newest first, narrowed by an optional query against the
    # name or the display name.
    # Platform operators see every organization; everyone else sees their own. Pass
    # the cursor from the previous page to continue; an empty cursor in the answer
    # means there is nothing more.
    # THE SCOPE IS THE HANDLER'S OWN, so it holds at every endpoint. The Guard refuses
    # a bearerless request before this runs, but the MCP server carries a typed op to
    # its handler with no middleware in front of it — a handler that read no
    # principal would answer such a caller with the whole registry. Reading the
    # principal here is what makes the answer the same one over both.
    listOrganizations(req: ListOrganizationsInput) returns (rep: ListOrganizationsOutput)
    # Returns who is currently signed in to an organization, newest first, and
    # can be narrowed to one person or one application. It is what you read before
    # signing someone out.
    # Which organization comes from your credentials, not from the request: you read
    # your own and no one else's. A session row names a live account and the
    # applications it is signed in to, so the tenant is decided here rather than
    # taken from the query.
    listSessions(req: ListSessionsIn) returns (rep: ListSessionsOut)
    # Returns the access tokens issued in your organization, newest
    # first, and can be narrowed to one organization. Use it to see what is currently
    # authorized before revoking anything.
    listTokens(req: listTokensIn) returns (rep: listTokensOut)
    # Returns the passkeys and security keys registered to
    # one person, newest first — which device each lives on and when it was
    # registered.
    # Yours by default. Name somebody else and you get them only if you already
    # administer their account, which is the same authority that governs reading
    # their user record — so this list can never show more people than the surface
    # beside it already does.
    # There is no organization-wide list, by design. Scoping to the ORG would hand an
    # org admin every member's credential rows in one answer and a SuperAdmin every
    # tenant's, while a plain member could not read even their own (an unnamed target
    # fails the Guard's tenant rule). One scope answers both halves cleanly: the
    # answer is a person's, and the caller is that person unless they say otherwise
    # and may.
    listWebauthnCredentials(req: listWebauthnCredentialsIn) returns (rep: listWebauthnCredentialsOut)
    # Records an audit entry, so activity from your own systems lands in the
    # same trail as everything the Hanzo Cloud records for you.
    post_iam_audit_logs(req: Input) returns (rep: AuditLog)
    # Adds a signing certificate your applications can verify tokens against
    # — the call you make to stage the next one before a rotation. A name already
    # used in your organization is refused.
    # It registers the certificate's IDENTITY: its name (which is the JWKS `kid`),
    # its algorithm, its expiry. Key material does not travel this way and cannot:
    # the private key is not part of the Cert's JSON, so it is neither served here
    # nor accepted here. It is supplied to the process by the deployment, under the
    # name registered here (internal/keyring). Staging a rotation is therefore two
    # halves — this call names the key, and the deployment provides it.
    post_iam_certs(req: Cert) returns (rep: Cert)
    # Issues an invitation to join your organization — the code or link a new
    # member redeems, with the role they arrive holding and the date it stops
    # working. A name already used in the organization is refused.
    post_iam_invitations(req: invitations_Input) returns (rep: Invitation)
    # Issues an API key. A standard key comes back as a publishable half you
    # may ship in client code and a secret half you must not — the secret is shown
    # once, at creation, and cannot be retrieved afterwards. A publish-scoped key is
    # issued with the publishable half only, so there is no secret to leak.
    # A name already used in your organization is refused rather than reissued, so
    # creating twice never silently invalidates a key that is in production.
    post_iam_keys(req: Key) returns (rep: Key)
    # Grants a permission — the call that gives a person or a role the ability to
    # do something. Adding refuses to overwrite a grant that already exists, so
    # widening an existing one is an update, never an accident.
    post_iam_permissions(req: Permission) returns (rep: Permission)
    # Makes a project inside your organization — the scope people pick
    # between when their work is separated by product or client rather than by team.
    # A name already used in the organization is refused.
    post_iam_projects(req: projects_Input) returns (rep: Project)
    # Makes a role — a named group of people that permissions are granted to.
    # Granting to a role rather than to each person is what keeps access correct as
    # your team changes: add someone to the role and they inherit everything it can
    # do. A name already used in your organization is refused.
    post_iam_roles(req: roles_Input) returns (rep: Role)
    # Makes a team — a named set of people that roles and permissions grant
    # to. Granting to a team rather than to each person keeps access correct as
    # people come and go: add someone and they inherit what the team can do. A name
    # already used in your organization is refused.
    post_iam_teams(req: teams_Input) returns (rep: Team)
    # Makes a workspace inside your organization — the scope a team works in,
    # alongside projects rather than instead of them. A name already used in the
    # organization is refused.
    post_iam_workspaces(req: workspaces_Input) returns (rep: Workspace)
    # Corrects an audit entry. The trail is append-only in normal operation
    # and nothing in the Hanzo Cloud rewrites it — this exists for an administrator
    # to correct an entry their own systems recorded wrongly.
    put_iam_audit_logs_by_owner_by_name(req: Input) returns (rep: AuditLog)
    # Changes a signing certificate's settings. What it is called does not
    # change, and neither does when it was added.
    # A PUT here is a METADATA edit — display name, expiry, provider. It overlays
    # only the fields the request actually SET onto the loaded row: a field the JSON
    # omits (or leaves at its zero value) keeps what the row holds, rather than
    # blanking it. That is load-bearing, not a nicety. A read serves the public
    # Certificate (Mask hides only PrivateKey and AccessSecret), so a client that
    # reads a cert, changes one field, and writes it back sends the masked halves
    # empty and every other field it did not touch at its zero value — and the old
    # full-struct overlay wrote all of those blanks back. Blanking CryptoAlgorithm
    # alone drops the cert from the JWKS (oidc.Publishes turns false), so every
    # token under its `kid` stops verifying; blanking Provider/Account/ExpireTime
    # breaks ACME renewal and expiry — all from a request that only meant to rename
    # it. Absent-or-zero means "unchanged", so the deployment (key) and a rotation
    # (cert) remain the only way key or published material changes; the metadata API
    # cannot clear it.
    # The overlay is generic — it copies every set field, so a field nobody has added
    # yet is carried without a line here — and leaves three things the request may
    # not move: the bound Model (id, createdAt, key, snapshot), the natural key
    # (owner/name address the row, they do not mutate it), and the creation stamp.
    put_iam_certs_by_owner_by_name(req: Cert) returns (rep: Cert)
    # Changes an invitation's terms — the role it grants, how many may redeem
    # it, or when it expires. What it is called does not change.
    put_iam_invitations_by_owner_by_name(req: invitations_Input) returns (rep: Invitation)
    # Changes what a key is called or what it may reach. The credential
    # itself is not reissued — the key in your deployment keeps working.
    put_iam_keys_by_owner_by_name(req: Key) returns (rep: Key)
    # Changes who a permission grants to, what it allows, or the resources it
    # covers. Access changes as soon as the write lands. What the permission is
    # called does not change, and neither does when it was created.
    put_iam_permissions_by_owner_by_name(req: Permission) returns (rep: Permission)
    # Changes a project's settings. What it is called does not change, and
    # neither does when it was created.
    put_iam_projects_by_owner_by_name(req: projects_Input) returns (rep: Project)
    # Changes who is in a role, or which roles it includes. Access changes for
    # everyone in it as soon as the write lands. What the role is called does not
    # change, and neither does when it was created.
    put_iam_roles_by_owner_by_name(req: roles_Input) returns (rep: Role)
    # Changes who is in a team. Access changes for
    # everyone in it as soon as the write lands. The name and the created stamp do
    # not change.
    put_iam_teams_by_name(req: teams_Input) returns (rep: Team)
    # Changes a workspace's settings. What it is called does not change, and
    # neither does when it was created.
    put_iam_workspaces_by_owner_by_name(req: workspaces_Input) returns (rep: Workspace)
    # Changes how an organization appears across Hanzo: the square mark
    # beside its name, as an uploaded image or as a single emoji. Sending an image
    # clears the emoji and sending an emoji clears the image — an organization has
    # one mark, not a preference order — and sending neither clears both, which is
    # how it goes back to being drawn as its initial.
    # An image is an https link or the bytes inline as a data URL, up to 96 KiB.
    # Anyone who administers the organization may set this; it is not reserved to
    # the platform.
    # It writes the two fields onto the stored row and touches nothing else, which
    # update cannot do: update replaces the whole record, and a record read back
    # first arrives masked, so a read-modify-write through it would persist the mask
    # over the organization's own credential settings.
    setOrganizationAvatar(req: SetAvatarInput) returns (rep: Organization)
    # Changes how an organization reads: its display name, its website
    # and its favicon.
    # IT EXISTS FOR THE REASON SetAvatar DOES, and the reason is worth stating
    # because the obvious alternative is a trap. Update REPLACES the whole record,
    # so a caller that wants to change one field has to send every other field
    # back — and a record read back first arrives MASKED, so the read half of that
    # read-modify-write hands you "***" for the master password and the salt, and
    # the write half stores it. Renaming an organization through Update therefore
    # costs it its credential settings; sending only the new name costs it
    # everything else. Neither is a rename.
    # So this writes the fields it names and touches nothing else. A nil pointer is
    # not sent and not changed; an empty string is sent and clears the field.
    setOrganizationProfile(req: SetProfileInput) returns (rep: Organization)
    # Changes an organization's display, its defaults and the sign-in rules
    # everyone in it inherits. Which organization it is does not change, and neither
    # does when it was created.
    updateOrganization(req: UpdateOrganizationInput) returns (rep: Organization)
    # Names the browsers a session keeps — signing out the ones you leave off
    # while the session itself stays live. A session that does not exist is reported
    # as missing rather than created.
    updateSession(req: UpdateSessionIn) returns (rep: Session)
    # Changes an access token's scope or expiry.
    # A token that is not there answers "nothing changed" rather than an error, so
    # the call is safe to repeat.
    updateToken(req: Token) returns (rep: tokenMutation)
    # Renames a registered passkey or security key, so a
    # person can tell their devices apart.
    # A credential that is not there answers "nothing changed" rather than an error,
    # so the call is safe to repeat.
    updateWebauthnCredential(req: WebauthnCredential) returns (rep: webauthnCredentialMutationResult)
}

# ---------------------------------------------------------------------
# 72 op(s) here. What follows is what this schema does not carry.
#
# blocked (32) — the op is absent; the field has no wire form:
#   addProvider  Provider.HttpHeaders  map[string]string  (map)
#   addProvider  Provider.UserMapping  map[string]string  (map)
#   addProvider  providerResult.Provider  schema.Provider  (reaches one)
#   deleteProvider  mutationResult.Provider  schema.Provider  (reaches one)
#   getProvider  providerResult  providers.providerResult  (reaches one)
#   get_iam_applications  ApplicationListResult.Applications  []*schema.Application  (no wire form)
#   get_iam_applications_by_owner_by_name  Application.Providers  []*schema.ProviderItem  (no wire form)
#   get_iam_auth_application  Answer.Response  httpx.Response  (reaches one)
#   get_iam_auth_methods  Answer  httpx.Answer  (reaches one)
#   get_iam_memberships  Answer  httpx.Answer  (reaches one)
#   get_iam_scim_v2_resourcetypes  listResponse.Resources  []interface {}  (no wire form)
#   get_iam_scim_v2_schemas  listResponse  scim.listResponse  (reaches one)
#   get_iam_service-accounts  Answer  httpx.Answer  (reaches one)
#   get_iam_users  ListOutput.Users  []*schema.User  (no wire form)
#   get_iam_users_by_owner_by_name  User.Properties  map[string]string  (map)
#   listProviders  listProvidersOut.Providers  []*schema.Provider  (no wire form)
#   post_iam_applications  Application  schema.Application  (reaches one)
#   post_iam_applications  Application  schema.Application  (reaches one)
#   post_iam_assume  Answer  httpx.Answer  (reaches one)
#   post_iam_release  Answer  httpx.Answer  (reaches one)
#   post_iam_users  CreateInput.User  schema.User  (reaches one)
#   post_iam_users  User  schema.User  (reaches one)
#   put_iam_account  Answer  httpx.Answer  (reaches one)
#   put_iam_applications_by_owner_by_name  Application  schema.Application  (reaches one)
#   put_iam_applications_by_owner_by_name  Application  schema.Application  (reaches one)
#   put_iam_password  Answer  httpx.Answer  (reaches one)
#   put_iam_users_by_owner_by_name  UpdateInput.User  schema.User  (reaches one)
#   put_iam_users_by_owner_by_name  User  schema.User  (reaches one)
#   updateProvider  Provider  schema.Provider  (reaches one)
#   updateProvider  mutationResult  providers.mutationResult  (reaches one)
#   upsertApplication  reply.Data  interface {}  (any)
#   upsertUser  reply  bootstrap.reply  (reaches one)
#
# opaque (42) — crosses, arrives without its name:
#   AuditLog.Model  orm.Model[github.com/hanzoai/iam/pkg/schema.AuditLog]
#   Cert.Model  orm.Model[github.com/hanzoai/iam/pkg/schema.Cert]
#   CreateOrganizationInput.Organization  schema.Organization
#   Invitation.Model  orm.Model[github.com/hanzoai/iam/pkg/schema.Invitation]
#   Key.Model  orm.Model[github.com/hanzoai/iam/pkg/schema.Key]
#   ListOrganizationsOutput.Organizations  schema.Organization (list element)
#   ListOutput.AuditLogs  schema.AuditLog (list element)
#   ListResponse.Keys  schema.Key (list element)
#   ListSessionsOut.Sessions  schema.Session (list element)
#   Organization.AccountItems  schema.AccountItem (list element)
#   Organization.MfaItems  schema.MfaItem (list element)
#   Organization.Model  orm.Model[github.com/hanzoai/iam/pkg/schema.Organization]
#   Organization.ThemeData  schema.ThemeData
#   Permission.Model  orm.Model[github.com/hanzoai/iam/pkg/schema.Permission]
#   Project.Model  orm.Model[github.com/hanzoai/iam/pkg/schema.Project]
#   Role.Model  orm.Model[github.com/hanzoai/iam/pkg/schema.Role]
#   Session.Model  orm.Model[github.com/hanzoai/iam/pkg/schema.Session]
#   Team.Model  orm.Model[github.com/hanzoai/iam/pkg/schema.Team]
#   Token.Model  orm.Model[github.com/hanzoai/iam/pkg/schema.Token]
#   UpdateOrganizationInput.Organization  schema.Organization
#   WebauthnCredential.Model  orm.Model[github.com/hanzoai/iam/pkg/schema.WebauthnCredential]
#   Workspace.Model  orm.Model[github.com/hanzoai/iam/pkg/schema.Workspace]
#   certs_ListOutput.Certs  schema.Cert (list element)
#   config.Bulk  scim.bulk
#   config.Etag  scim.toggle
#   config.Filter  scim.filter
#   config.Password  scim.toggle
#   config.Patch  scim.toggle
#   config.Schemes  scim.scheme (list element)
#   config.Sort  scim.toggle
#   invitations_ListOutput.Invitations  schema.Invitation (list element)
#   listTokensOut.Tokens  schema.Token (list element)
#   listWebauthnCredentialsOut.WebauthnCredentials  schema.WebauthnCredential (list element)
#   permission_ListResponse.Permissions  schema.Permission (list element)
#   projects_ListOutput.Projects  schema.Project (list element)
#   roles_ListOutput.Roles  schema.Role (list element)
#   teams_ListOutput.Teams  schema.Team (list element)
#   tokenMutation.Token  schema.Token
#   tokenResult.Token  schema.Token
#   webauthnCredentialMutationResult.WebauthnCredential  schema.WebauthnCredential
#   webauthnCredentialResult.WebauthnCredential  schema.WebauthnCredential
#   workspaces_ListOutput.Workspaces  schema.Workspace (list element)
#
# renamed (5) — spelled differently here than on every other surface:
#   delete_iam_audit-logs_by_owner_by_name  ->  delete_iam_audit_logs_by_owner_by_name
#   get_iam_audit-logs  ->  get_iam_audit_logs
#   get_iam_audit-logs_by_owner_by_name  ->  get_iam_audit_logs_by_owner_by_name
#   post_iam_audit-logs  ->  post_iam_audit_logs
#   put_iam_audit-logs_by_owner_by_name  ->  put_iam_audit_logs_by_owner_by_name
