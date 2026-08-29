# Generated from typed ops. Do not edit.
# Every struct and method below is derived from one op's In and Out, and
# every offset from the layout the op-call plane encodes against.

package o11y

struct O11yAPIKeyCreateIn {
    ServiceAccountID text @0
    Name             text @8
    ExpiresAt        u64  @16
}

struct O11yAPIKeyCreateOut {
    Status text  @0
    Data   bytes @8
}

struct O11yAPIKeyRevokeIn {
    ID    text @0
    KeyID text @8
}

struct O11yAPIKeyUpdateIn {
    ServiceAccountID text @0
    KeyID            text @8
    Name             text @16
    ExpiresAt        u64  @24
}

struct O11yAPIKeysIn {
    ID text @0
}

struct O11yAPIKeysOut {
    Status text        @0
    Data   list<bytes> @8
}

struct O11yAccountRef {
    CloudProvider text @0
    ID            text @8
}

struct O11yAccountServiceRef {
    CloudProvider text @0
    ID            text @8
    ServiceID     text @16
}

struct O11yAck {
    Status text @0
}

struct O11yAggregateAttributesIn {
    DataSource        text @0
    AggregateOperator text @8
    SearchText        text @16
    Limit             i64  @24
}

struct O11yAggregateAttributesOut {
    Status text  @0
    Data   bytes @8
}

struct O11yAnalyzeIn {
    Query     text @0
    QueryType text @8
}

struct O11yAnalyzeOut {
    Status text  @0
    Data   bytes @8
}

struct O11yApdexIn {
    Services text @0
}

struct O11yApdexOut {
    Status text        @0
    Data   list<bytes> @8
}

struct O11yApdexSetIn {
    ServiceName        text @0
    Threshold          f64  @8
    ExcludeStatusCodes text @16
}

struct O11yApdexSetOut {
    Status text  @0
    Data   bytes @8
}

struct O11yAttributeKeysIn {
    DataSource         text @0
    AggregateOperator  text @8
    AggregateAttribute text @16
    SearchText         text @24
    TagType            text @32
    Limit              i64  @40
}

struct O11yAttributeKeysOut {
    Status text  @0
    Data   bytes @8
}

struct O11yAttributeValuesIn {
    DataSource                 text @0
    AggregateOperator          text @8
    AggregateAttribute         text @16
    AttributeKey               text @24
    FilterAttributeKeyDataType text @32
    SearchText                 text @40
    TagType                    text @48
    Limit                      i64  @56
}

struct O11yBulkInviteIn {
    Invites list<bytes> @0
}

struct O11yChangePasswordIn {
    OldPassword text @0
    NewPassword text @8
}

struct O11yChannelOut {
    Status text  @0
    Data   bytes @8
}

struct O11yChannelRef {
    ID text @0
}

struct O11yChannelsOut {
    Status text        @0
    Data   list<bytes> @8
}

struct O11yCheckOut {
    Status text        @0
    Data   list<bytes> @8
}

struct O11yCloudProviderRef {
    CloudProvider text @0
}

struct O11yConnectionStatusIn {
    IntegrationID   text @0
    LookbackSeconds i64  @8
}

struct O11yConnectionStatusOut {
    Status text  @0
    Data   bytes @8
}

struct O11yCreateAccountIn {
    CloudProvider   text  @0
    PostableAccount bytes @8
}

struct O11yCreateAccountOut {
    Status text  @0
    Data   bytes @8
}

struct O11yCreateLimitIn {
    KeyID                     text  @0
    PostableIngestionKeyLimit bytes @8
}

struct O11yCreatedIngestionKeyOut {
    Status text  @0
    Data   bytes @8
}

struct O11yCreatedLimitOut {
    Status text  @0
    Data   bytes @8
}

struct O11yCreatedOut {
    Status text  @0
    Data   bytes @8
}

struct O11yCredentialsOut {
    Status text  @0
    Data   bytes @8
}

struct O11yDashboardIDIn {
    ID text @0
}

struct O11yDashboardListForUserOut {
    Status text  @0
    Data   bytes @8
}

struct O11yDashboardListOut {
    Status text  @0
    Data   bytes @8
}

struct O11yDashboardListParams {
    Query  text @0
    Sort   text @8
    Order  text @16
    Limit  i64  @24
    Offset i64  @32
}

struct O11yDashboardOut {
    Status text  @0
    Data   bytes @8
}

struct O11yDashboardPatchIn {
    ID  text        @0
    Ops list<bytes> @8
}

struct O11yDashboardPostable {
    SchemaVersion text        @0
    Image         text        @8
    Name          text        @16
    GenerateName  bool        @24
    Tags          list<bytes> @32
    Spec          bytes       @40
}

struct O11yDashboardUpdateIn {
    ID                     text  @0
    O11yDashboardUpdatable bytes @8
}

struct O11yDashboardViewListOut {
    Status text  @0
    Data   bytes @8
}

struct O11yDashboardViewOut {
    Status text  @0
    Data   bytes @8
}

struct O11yDashboardViewPostable {
    Name text  @0
    Data bytes @8
}

struct O11yDashboardViewUpdateIn {
    ID                        text  @0
    O11yDashboardViewPostable bytes @8
}

struct O11yDependencyGraphIn {
    Start text        @0
    End   text        @8
    Tags  list<bytes> @16
}

struct O11yDeprecatedUserOut {
    Status text  @0
    Data   bytes @8
}

struct O11yDeprecatedUserUpdate {
    ID          text @0
    DisplayName text @8
    Role        text @16
}

struct O11yDeprecatedUsersOut {
    Status text        @0
    Data   list<bytes> @8
}

struct O11yDiscoverIn {
    Project      text        @0
    Filters      list<bytes> @8
    Aggregations list<text>  @16
    GroupBy      list<text>  @24
    Period       text        @32
    OrderBy      text        @40
    OrderDir     text        @48
    Limit        i64         @56
}

struct O11yDomainRef {
    ID text @0
}

struct O11yDomainsIn {
    Start    u64         @0
    End      u64         @8
    ShowIP   bool        @16
    Domain   text        @24
    Endpoint text        @32
    Filter   bytes       @40
    GroupBy  list<bytes> @48
}

struct O11yDowntimeRef {
    ID text @0
}

struct O11yDowntimeScheduleOut {
    Status text  @0
    Data   bytes @8
}

struct O11yDowntimeSchedulesOut {
    Status text        @0
    Data   list<bytes> @8
}

struct O11yDowntimeUpdateIn {
    ID                         text  @0
    PostablePlannedMaintenance bytes @8
}

struct O11yEmailPasswordSessionIn {
    Email    text @0
    Password text @8
    OrgID    text @16
}

struct O11yErrorIssueOut {
    Status text  @0
    Data   bytes @8
}

struct O11yErrorIssueRef {
    ID text @0
}

struct O11yErrorIssuesIn {
    Status      text @0
    Level       text @8
    Environment text @16
    ServiceName text @24
    Query       text @32
    Sort        text @40
    Offset      i64  @48
    Limit       i64  @56
}

struct O11yErrorIssuesOut {
    Status text  @0
    Data   bytes @8
}

struct O11yErrorLookupIn {
    Timestamp text @0
    GroupID   text @8
    ErrorID   text @16
}

struct O11yErrorUpdateIssueIn {
    ID       text @0
    Status   text @8
    Assignee text @16
}

struct O11yErrorWithSpan {
    ErrorID             text  @0
    ExceptionType       text  @8
    ExceptionStacktrace text  @16
    ExceptionEscaped    bool  @24
    ExceptionMsg        text  @32
    Timestamp           bytes @40
    SpanID              text  @48
    TraceID             text  @56
    ServiceName         text  @64
    GroupID             text  @72
}

struct O11yErrorsCountIn {
    Start         text        @0
    End           text        @8
    ServiceName   text        @16
    ExceptionType text        @24
    Tags          list<bytes> @32
}

struct O11yErrorsListIn {
    Start         text        @0
    End           text        @8
    Limit         i64         @16
    OrderParam    text        @24
    Order         text        @32
    Offset        i64         @40
    ServiceName   text        @48
    ExceptionType text        @56
    Tags          list<bytes> @64
}

struct O11yFieldCatalogOut {
    Selected    list<bytes> @0
    Interesting list<bytes> @8
}

struct O11yFieldKeysIn {
    Signal          text @0
    Source          text @8
    Limit           i64  @16
    StartUnixMilli  i64  @24
    EndUnixMilli    i64  @32
    FieldContext    text @40
    FieldDataType   text @48
    MetricName      text @56
    MetricNamespace text @64
    SearchText      text @72
}

struct O11yFieldSetting {
    Name             text @0
    DataType         text @8
    Type             text @16
    Selected         bool @24
    Index            text @32
    IndexGranularity i64  @40
}

struct O11yFieldValuesIn {
    Signal          text @0
    Source          text @8
    Limit           i64  @16
    StartUnixMilli  i64  @24
    EndUnixMilli    i64  @32
    FieldContext    text @40
    FieldDataType   text @48
    MetricName      text @56
    MetricNamespace text @64
    SearchText      text @72
    Name            text @80
    ExistingQuery   text @88
}

struct O11yFieldValuesOut {
    Status text  @0
    Data   bytes @8
}

struct O11yFilterSuggestionsIn {
    DataSource      text @0
    SearchText      text @8
    ExistingFilter  text @16
    AttributesLimit i64  @24
    ExamplesLimit   i64  @32
}

struct O11yForgotPasswordIn {
    OrgID           text @0
    Email           text @8
    FrontendBaseURL text @16
}

struct O11yFunnelCreateIn {
    Name      text @0
    Timestamp i64  @8
}

struct O11yFunnelDeleteOut {
    Status text @0
}

struct O11yFunnelRef {
    FunnelID text @0
}

struct O11yFunnelStepWindowIn {
    FunnelID              text  @0
    StepTransitionRequest bytes @8
}

struct O11yFunnelUpdateIn {
    FunnelID    text @0
    Name        text @8
    Description text @16
    Timestamp   i64  @24
}

struct O11yFunnelWindowIn {
    FunnelID  text  @0
    TimeRange bytes @8
}

struct O11yGetServiceIn {
    CloudProvider      text @0
    ServiceID          text @8
    CloudIntegrationID text @16
}

struct O11yGettableHostOut {
    Status text  @0
    Data   bytes @8
}

struct O11yGlobalConfigOut {
    Status text  @0
    Data   bytes @8
}

struct O11yHealthIn {
    Live bool @0
}

struct O11yHealthOut {
    Status text @0
}

struct O11yIdentifiableOut {
    Status text  @0
    Data   bytes @8
}

struct O11yInfraAttributeKeysIn {
    DataSource         text @0
    AggregateOperator  text @8
    AggregateAttribute text @16
    SearchText         text @24
    TagType            text @32
    Limit              i64  @40
}

struct O11yInfraAttributeKeysOut {
    Status text  @0
    Data   bytes @8
}

struct O11yInfraAttributeValuesIn {
    DataSource                 text @0
    AggregateOperator          text @8
    AggregateAttribute         text @16
    AttributeKey               text @24
    FilterAttributeKeyDataType text @32
    SearchText                 text @40
    TagType                    text @48
    Limit                      i64  @56
}

struct O11yInfraChecksIn {
    Type text @0
}

struct O11yInfraChecksOut {
    Status text  @0
    Data   bytes @8
}

struct O11yIngestionKeyRef {
    KeyID text @0
}

struct O11yIngestionKeysIn {
    Page    i64 @0
    PerPage i64 @8
}

struct O11yIngestionKeysOut {
    Status text  @0
    Data   bytes @8
}

struct O11yInstallOut {
    Status text  @0
    Data   bytes @8
}

struct O11yIntegrationAck {
    Status text @0
}

struct O11yIntegrationRef {
    IntegrationID text @0
}

struct O11yIntegrationsListOut {
    Status text  @0
    Data   bytes @8
}

struct O11yInviteIn {
    Name            text @0
    Email           text @8
    Role            text @16
    FrontendBaseUrl text @24
}

struct O11yInviteOut {
    Status text  @0
    Data   bytes @8
}

struct O11yLLMAnnotationOut {
    Status text  @0
    Data   bytes @8
}

struct O11yLLMAnnotationsOut {
    Status text  @0
    Data   bytes @8
}

struct O11yLLMAnnotationsQuery {
    TraceID text @0
    Queue   text @8
    Status  text @16
    Offset  i64  @24
    Limit   i64  @32
}

struct O11yLLMIngestAnnotation {
    TraceID       text @0
    ObservationID text @8
    Queue         text @16
    Content       text @24
    Status        text @32
}

struct O11yLLMIngestScore {
    TraceID       text @0
    ObservationID text @8
    Name          text @16
    Value         f64  @24
    StringValue   text @32
    DataType      text @40
    Comment       text @48
    Source        text @56
}

struct O11yLLMObservationsOut {
    Status text  @0
    Data   bytes @8
}

struct O11yLLMPricingRuleOut {
    Status text  @0
    Data   bytes @8
}

struct O11yLLMPricingRuleRef {
    ID text @0
}

struct O11yLLMPricingRulesOut {
    Status text  @0
    Data   bytes @8
}

struct O11yLLMPricingRulesQuery {
    Search     text @0
    IsOverride text @8
    Offset     i64  @16
    Limit      i64  @24
}

struct O11yLLMScoreOut {
    Status text  @0
    Data   bytes @8
}

struct O11yLLMScoreRef {
    ID text @0
}

struct O11yLLMScoresOut {
    Status text  @0
    Data   bytes @8
}

struct O11yLLMScoresQuery {
    TraceID       text @0
    ObservationID text @8
    Name          text @16
    Source        text @24
    Offset        i64  @32
    Limit         i64  @40
}

struct O11yLLMSessionsOut {
    Status text  @0
    Data   bytes @8
}

struct O11yLLMTracesOut {
    Status text  @0
    Data   bytes @8
}

struct O11yLLMUpdatablePricingRules {
    Rules list<bytes> @0
}

struct O11yLLMUsersOut {
    Status text  @0
    Data   bytes @8
}

struct O11yLLMViewQuery {
    Start     i64  @0
    End       i64  @8
    TraceID   text @16
    SessionID text @24
    UserID    text @32
    Name      text @40
    Model     text @48
    Offset    i64  @56
    Limit     i64  @64
}

struct O11yLimitRef {
    LimitID text @0
}

struct O11yListDowntimeSchedulesIn {
    Active    text @0
    Recurring text @8
}

struct O11yListIntegrationsIn {
    IsInstalled text @0
}

struct O11yListServicesMetadataIn {
    CloudProvider      text @0
    CloudIntegrationID text @8
}

struct O11yLogPipelinesIn {
    Version text @0
}

struct O11yLogPromoteOut {
    Status text @0
}

struct O11yLogPromotedOut {
    Status text        @0
    Data   list<bytes> @8
}

struct O11yLogRecordsIn {
    Limit          i64 @0
    TimestampStart i64 @8
    TimestampEnd   i64 @16
}

struct O11yLogsIn {
    Project text @0
    Query   text @8
    Period  text @16
    Limit   i64  @24
}

struct O11yMessage {
    Data text @0
}

struct O11yMetricAckOut {
    Status text @0
}

struct O11yMetricAlertsOut {
    Status text  @0
    Data   bytes @8
}

struct O11yMetricAttributesIn {
    MetricName text @0
    Start      i64  @8
    End        i64  @16
}

struct O11yMetricAttributesOut {
    Status text  @0
    Data   bytes @8
}

struct O11yMetricDashboardsOut {
    Status text  @0
    Data   bytes @8
}

struct O11yMetricHighlightsOut {
    Status text  @0
    Data   bytes @8
}

struct O11yMetricInspectIn {
    MetricName text  @0
    Start      i64   @8
    End        i64   @16
    Filter     bytes @24
}

struct O11yMetricListIn {
    Start      i64  @0
    End        i64  @8
    Limit      i64  @16
    SearchText text @24
    Source     text @32
}

struct O11yMetricListOut {
    Status text  @0
    Data   bytes @8
}

struct O11yMetricMetadataIn {
    MetricName  text @0
    ServiceName text @8
}

struct O11yMetricMetadataOut {
    Status text  @0
    Data   bytes @8
}

struct O11yMetricMetadataSaveIn {
    MetricName  text @0
    Type        text @8
    Description text @16
    Unit        text @24
    Temporality text @32
    IsMonotonic bool @40
}

struct O11yMetricNameIn {
    MetricName text @0
}

struct O11yMetricOnboardingOut {
    Status text  @0
    Data   bytes @8
}

struct O11yMetricStatsIn {
    Filter  bytes @0
    Start   i64   @8
    End     i64   @16
    Limit   i64   @24
    Offset  i64   @32
    OrderBy bytes @40
}

struct O11yMetricStatsOut {
    Status text  @0
    Data   bytes @8
}

struct O11yMetricTreemapIn {
    Filter bytes @0
    Start  i64   @8
    End    i64   @16
    Limit  i64   @24
    Mode   text  @32
}

struct O11yMetricTreemapOut {
    Status text  @0
    Data   bytes @8
}

struct O11yMetricsQueryRangeIn {
    Start   text @0
    End     text @8
    Step    text @16
    Query   text @24
    Stats   text @32
    Timeout text @40
}

struct O11yMetricsQueryRangeOut {
    Status text  @0
    Data   bytes @8
}

struct O11yMyServiceAccountUpdateIn {
    Name text @0
}

struct O11yNextPrevErrorIDs {
    NextErrorID   text  @0
    NextTimestamp bytes @8
    PrevErrorID   text  @16
    PrevTimestamp bytes @24
    GroupID       text  @32
}

struct O11yOnboardingOut {
    Status text  @0
    Data   bytes @8
}

struct O11yOperationsIn {
    Start   text        @0
    End     text        @8
    Service text        @16
    Tags    list<bytes> @24
    Limit   i64         @32
}

struct O11yOperationsOut {
    Status text        @0
    Data   list<bytes> @8
}

struct O11yOrganization {
    CreatedAt   bytes @0
    UpdatedAt   bytes @8
    ID          text  @16
    Name        text  @24
    Alias       text  @32
    Key         u32   @40
    DisplayName text  @48
}

struct O11yOrganizationOut {
    Status text  @0
    Data   bytes @8
}

struct O11yOverallStateTransitionsOut {
    Status text        @0
    Data   list<bytes> @8
}

struct O11yPostableUser {
    DisplayName     text        @0
    Email           text        @8
    FrontendBaseUrl text        @16
    UserRoles       list<bytes> @24
}

struct O11yPreferenceRef {
    Name text @0
}

struct O11yPromQueryIn {
    Query   text @0
    Time    text @8
    Stats   text @16
    Timeout text @24
}

struct O11yPromQueryOut {
    Status text  @0
    Data   bytes @8
}

struct O11yPublicDashboardDataOut {
    Status text  @0
    Data   bytes @8
}

struct O11yPublicDashboardOut {
    Status text  @0
    Data   bytes @8
}

struct O11yPublicDashboardWriteIn {
    ID                       text  @0
    O11yPublicDashboardWrite bytes @8
}

struct O11yQueryRangeFormatOut {
    Status text  @0
    Data   bytes @8
}

struct O11yQueryRangeOut {
    Status text  @0
    Data   bytes @8
}

struct O11yQueryRangePreviewOut {
    Status text  @0
    Data   bytes @8
}

struct O11yQueueChecksOut {
    Status text        @0
    Data   list<bytes> @8
}

struct O11yQuickFiltersOut {
    Status text        @0
    Data   list<bytes> @8
}

struct O11yReductionRuleCreateIn {
    MetricName text       @0
    MatchType  text       @8
    Labels     list<text> @16
}

struct O11yReductionRuleListIn {
    OrderBy    text @0
    Order      text @8
    Search     text @16
    MetricName text @24
    Offset     i64  @32
    Limit      i64  @40
}

struct O11yReductionRuleListOut {
    Status text  @0
    Data   bytes @8
}

struct O11yReductionRuleOut {
    Status text  @0
    Data   bytes @8
}

struct O11yReductionRulePreviewIn {
    MetricName text       @0
    MatchType  text       @8
    Labels     list<text> @16
    LookbackMs i64        @24
}

struct O11yReductionRulePreviewOut {
    Status text  @0
    Data   bytes @8
}

struct O11yReductionRuleRef {
    ID text @0
}

struct O11yReductionRuleSaveIn {
    ID        text       @0
    MatchType text       @8
    Labels    list<text> @16
}

struct O11yReductionStatsOut {
    Status text  @0
    Data   bytes @8
}

struct O11yRegisterIn {
    Name           text @0
    Email          text @8
    Password       text @16
    OrgDisplayName text @24
    OrgName        text @32
}

struct O11yRegisterOut {
    Status text  @0
    Data   bytes @8
}

struct O11yResetPasswordIn {
    Password text @0
    Token    text @8
}

struct O11yResetTokenOut {
    Status text  @0
    Data   bytes @8
}

struct O11yResetTokenRef {
    Token text @0
}

struct O11yRetentionOut {
    Version                  text        @0
    Status                   text        @8
    ExpectedLogsTTLHours     i64         @16
    ExpectedLogsMoveTTLHours i64         @24
    DefaultTTLDays           i64         @32
    TTLConditions            list<bytes> @40
    ColdStorageVolume        text        @48
    ColdStorageTTLDays       i64         @56
}

struct O11yRetentionSetIn {
    Type                    text        @0
    DefaultTTLDays          i64         @8
    TTLConditions           list<bytes> @16
    ColdStorageVolume       text        @24
    ColdStorageDurationDays i64         @32
}

struct O11yRetentionSetOut {
    Message text @0
}

struct O11yRoleCreateIn {
    Name              text        @0
    Description       text        @8
    TransactionGroups list<bytes> @16
}

struct O11yRoleCreateOut {
    Status text  @0
    Data   bytes @8
}

struct O11yRoleDeleteIn {
    ID text @0
}

struct O11yRoleGetIn {
    ID text @0
}

struct O11yRoleOut {
    Status text  @0
    Data   bytes @8
}

struct O11yRoleUpdateIn {
    ID                text        @0
    Description       text        @8
    TransactionGroups list<bytes> @16
}

struct O11yRoleUsersIn {
    ID text @0
}

struct O11yRolesOut {
    Status text        @0
    Data   list<bytes> @8
}

struct O11yRotateSessionIn {
    RefreshToken text @0
}

struct O11yRoutePoliciesOut {
    Status text        @0
    Data   list<bytes> @8
}

struct O11yRoutePolicyOut {
    Status text  @0
    Data   bytes @8
}

struct O11yRoutePolicyRef {
    ID text @0
}

struct O11yRoutePolicyUpdateIn {
    ID                  text  @0
    PostableRoutePolicy bytes @8
}

struct O11yRuleHistoryBaseIn {
    ID    text @0
    Start i64  @8
    End   i64  @16
}

struct O11yRuleHistoryFilterKeysIn {
    ID             text @0
    StartUnixMilli i64  @8
    EndUnixMilli   i64  @16
    SearchText     text @24
    Limit          i64  @32
}

struct O11yRuleHistoryFilterValuesIn {
    O11yRuleHistoryFilterKeysIn bytes @0
    Name                        text  @8
    ExistingQuery               text  @16
}

struct O11yRuleHistoryFilterValuesOut {
    Status text  @0
    Data   bytes @8
}

struct O11yRuleHistoryOverallStatusOut {
    Status text        @0
    Data   list<bytes> @8
}

struct O11yRuleHistoryTimelineIn {
    ID               text @0
    Start            i64  @8
    End              i64  @16
    State            text @24
    FilterExpression text @32
    Limit            i64  @40
    Order            text @48
    Cursor           text @56
}

struct O11yRuleRef {
    ID text @0
}

struct O11yRuleStateContributorsOut {
    Status text        @0
    Data   list<bytes> @8
}

struct O11ySavedViewCreateOut {
    Status text  @0
    Data   bytes @8
}

struct O11ySavedViewDeleteOut {
    Status text @0
}

struct O11ySavedViewListIn {
    SourcePage text @0
    Name       text @8
    Category   text @16
}

struct O11ySavedViewRef {
    ViewID text @0
}

struct O11ySearchIngestionKeysIn {
    Name    text @0
    Page    i64  @8
    PerPage i64  @16
}

struct O11ySentryEventRef {
    ID      text @0
    Project text @8
}

struct O11ySentryIssueEventsIn {
    ID      text @0
    Project text @8
    Limit   i64  @16
}

struct O11ySentryIssueRef {
    ID text @0
}

struct O11ySentryIssuesIn {
    Status      text @0
    Level       text @8
    Environment text @16
    ServiceName text @24
    Query       text @32
    Sort        text @40
    Offset      i64  @48
    Limit       i64  @56
    Project     text @64
    Period      text @72
}

struct O11ySentryPostableProject {
    Name     text @0
    Slug     text @8
    Platform text @16
}

struct O11ySentryProjectOut {
    Status text  @0
    Data   bytes @8
}

struct O11ySentryProjectRef {
    ID text @0
}

struct O11ySentryProjectsOut {
    Status text  @0
    Data   bytes @8
}

struct O11ySentryUpdateIssueIn {
    ID       text @0
    Status   text @8
    Assignee text @16
}

struct O11yServiceAccountCreateIn {
    Name text @0
}

struct O11yServiceAccountCreateOut {
    Status text  @0
    Data   bytes @8
}

struct O11yServiceAccountDeleteIn {
    ID text @0
}

struct O11yServiceAccountGetIn {
    ID text @0
}

struct O11yServiceAccountOut {
    Status text  @0
    Data   bytes @8
}

struct O11yServiceAccountRoleGrantIn {
    ServiceAccountID text @0
    RoleID           text @8
}

struct O11yServiceAccountRoleRevokeIn {
    ID     text @0
    RoleID text @8
}

struct O11yServiceAccountRolesIn {
    ID text @0
}

struct O11yServiceAccountRolesOut {
    Status text        @0
    Data   list<bytes> @8
}

struct O11yServiceAccountUpdateIn {
    ID   text @0
    Name text @8
}

struct O11yServiceAccountsOut {
    Status text        @0
    Data   list<bytes> @8
}

struct O11yServicesIn {
    Start text        @0
    End   text        @8
    Tags  list<bytes> @16
}

struct O11yServicesMetadataOut {
    Status text  @0
    Data   bytes @8
}

struct O11yServicesOut {
    Status text        @0
    Data   list<bytes> @8
}

struct O11ySessionContextIn {
    Email text @0
    Ref   text @8
}

struct O11ySessionContextOut {
    Status text  @0
    Data   bytes @8
}

struct O11ySetRoleIn {
    ID   text @0
    Name text @8
}

struct O11ySignalFiltersOut {
    Status text  @0
    Data   bytes @8
}

struct O11ySignalRef {
    Signal text @0
}

struct O11ySpanMapperCreateIn {
    GroupID            text  @0
    PostableSpanMapper bytes @8
}

struct O11ySpanMapperGroupOut {
    Status text  @0
    Data   bytes @8
}

struct O11ySpanMapperGroupRef {
    GroupID text @0
}

struct O11ySpanMapperGroupUpdateIn {
    GroupID                  text  @0
    UpdatableSpanMapperGroup bytes @8
}

struct O11ySpanMapperGroupsIn {
    Enabled bool @0
}

struct O11ySpanMapperGroupsOut {
    Status text  @0
    Data   bytes @8
}

struct O11ySpanMapperOut {
    Status text  @0
    Data   bytes @8
}

struct O11ySpanMapperRef {
    GroupID  text @0
    MapperID text @8
}

struct O11ySpanMapperUpdateIn {
    GroupID             text  @0
    MapperID            text  @8
    UpdatableSpanMapper bytes @16
}

struct O11ySpanMappersOut {
    Status text  @0
    Data   bytes @8
}

struct O11ySpanPercentileOut {
    Status text  @0
    Data   bytes @8
}

struct O11yStatsIn {
    Project text @0
    Field   text @8
    Period  text @16
}

struct O11yStatsOut {
    Status text  @0
    Data   bytes @8
}

struct O11ySubstituteVarsOut {
    Status text  @0
    Data   bytes @8
}

struct O11yTestNotificationOut {
    Status text  @0
    Data   bytes @8
}

struct O11yTestRuleOut {
    Status text  @0
    Data   bytes @8
}

struct O11yTokenOut {
    Status text  @0
    Data   bytes @8
}

struct O11yTopLevelOpsIn {
    Service text @0
    Start   text @8
    End     text @16
}

struct O11yTraceIn {
    ID      text @0
    Project text @8
}

struct O11yTraceSpansIn {
    TraceID         text @0
    SpanID          text @8
    LevelUp         i64  @16
    LevelDown       i64  @24
    SpanRenderLimit i64  @32
}

struct O11yTraceWaterfallIn {
    TraceID           text  @0
    PostableWaterfall bytes @8
}

struct O11yTracesIn {
    Project text @0
    Period  text @8
    Limit   i64  @16
}

struct O11yTracesOut {
    Status text  @0
    Data   bytes @8
}

struct O11yUpdatableQuickFilters {
    Signal  text        @0
    Filters list<bytes> @8
}

struct O11yUpdatableUser {
    DisplayName text @0
}

struct O11yUpdateAccountIn {
    CloudProvider    text  @0
    ID               text  @8
    UpdatableAccount bytes @16
}

struct O11yUpdateIngestionKeyIn {
    KeyID                text  @0
    PostableIngestionKey bytes @8
}

struct O11yUpdateLimitIn {
    LimitID                    text  @0
    UpdatableIngestionKeyLimit bytes @8
}

struct O11yUsageIn {
    Start   text @0
    End     text @8
    Step    i64  @16
    Service text @24
}

struct O11yUserRef {
    ID text @0
}

struct O11yUserRoleRef {
    ID     text @0
    RoleID text @8
}

struct O11yUserUpdate {
    ID          text @0
    DisplayName text @8
}

struct O11yUserWithRolesOut {
    Status text  @0
    Data   bytes @8
}

struct O11yUsersOut {
    Status text        @0
    Data   list<bytes> @8
}

struct O11yVersionOut {
    Version        text @0
    EE             text @8
    SetupCompleted bool @16
}

struct O11yWidgetQueryRangeIn {
    ID        text @0
    Idx       text @8
    StartTime text @16
    EndTime   text @24
}

struct O11yWidgetQueryRangeOut {
    Status text  @0
    Data   bytes @8
}

struct PostableHost {
    Name text @0
}

struct PostableIngestionKey {
    Name      text       @0
    Tags      list<text> @8
    ExpiresAt bytes      @16
}

struct PostablePlannedMaintenance {
    Name        text       @0
    Description text       @8
    Schedule    bytes      @16
    AlertIds    list<text> @24
    Scope       text       @32
}

struct PostableProfile {
    UsesOtel                     bool       @0
    HasExistingObservabilityTool bool       @1
    ExistingObservabilityTool    text       @8
    ReasonsForInterestInO11y     list<text> @16
    LogsScalePerDayInGB          i64        @24
    NumberOfServices             i64        @32
    NumberOfHosts                i64        @40
    WhereDidYouDiscoverO11y      text       @48
    TimelineForMigratingToO11y   text       @56
}

struct PostableRoutePolicy {
    Expression     text       @0
    ExpressionKind bytes      @8
    Channels       list<text> @16
    Name           text       @24
    Description    text       @32
    Tags           list<text> @40
}

struct PostableSpanMapperGroup {
    Name      text  @0
    Condition bytes @8
    Enabled   bool  @16
}

struct StatusSummary {
    PageTitle              text        @0
    PageURL                text        @8
    OngoingIncidents       list<bytes> @16
    InProgressMaintenances list<bytes> @24
    ScheduledMaintenances  list<bytes> @32
    CheckedAt              text        @40
}

struct UninstallIntegrationRequest {
    IntegrationId text @0
}

struct addItemsIn {
    ID    text        @0
    Items list<bytes> @8
}

struct annItemList {
    Data list<bytes> @0
    Meta bytes       @8
}

struct annItemView {
    ID            text @0
    QueueID       text @8
    ObjectType    text @16
    ObjectID      text @24
    TraceID       text @32
    ObservationID text @40
    SessionID     text @48
    Status        text @56
    Assignee      text @64
    CreatedAt     text @72
    UpdatedAt     text @80
    CompletedAt   text @88
}

struct annItemsCreated {
    Data list<bytes> @0
}

struct annPage {
    Page  i64 @0
    Limit i64 @8
}

struct annQueueDeleted {
    Deleted bool @0
}

struct annQueueDetailView {
    ID             text        @0
    Name           text        @8
    Description    text        @16
    ScoreConfigIDs list<text>  @24
    CreatedAt      text        @32
    UpdatedAt      text        @40
    PendingCount   i64         @48
    CompletedCount i64         @56
    Items          list<bytes> @64
}

struct annQueueList {
    Data list<bytes> @0
    Meta bytes       @8
}

struct annQueueRef {
    ID text @0
}

struct annQueueView {
    ID             text       @0
    Name           text       @8
    Description    text       @16
    ScoreConfigIDs list<text> @24
    CreatedAt      text       @32
    UpdatedAt      text       @40
}

struct availabilityIn {
    Range   i64 @0
    StepSec i64 @8
}

struct availabilityResponse {
    Range    bytes       @0
    Up       i64         @8
    Total    i64         @16
    Services list<bytes> @24
    Series   list<bytes> @32
}

struct createQueueReq {
    Name           text       @0
    Description    text       @8
    ScoreConfigIDs list<text> @16
}

struct listItemsIn {
    ID     text @0
    Status text @8
    Page   i64  @16
    Limit  i64  @24
}

struct metricsIn {
    Product text @0
    Range   i64  @8
    StepSec i64  @16
}

struct metricsResponse {
    Product text  @0
    Range   bytes @8
    Series  bytes @16
    Usage   bytes @24
    Summary bytes @32
}

struct statusIn {
    Product text @0
}

struct statusResult {
    Product     text        @0
    Up          bool        @8
    LatencyMs   i64         @16
    Source      text        @24
    Deployments list<bytes> @32
    CheckedAt   text        @40
}

struct tracesIn {
    Range         i64 @0
    Limit         i64 @8
    MinDurationMs i64 @16
}

struct tracesOut {
    SinceSec i64         @0
    Limit    i64         @8
    Count    i64         @16
    Traces   list<bytes> @24
}

struct updateItemIn {
    ID       text @0
    ItemID   text @8
    Status   text @16
    Assignee text @24
}

struct updateQueueIn {
    ID             text       @0
    Name           text       @8
    Description    text       @16
    ScoreConfigIDs list<text> @24
}

interface o11y {
    # Evaluates a batch of transactions — relation plus object — for
    # the authenticated caller and answers each with its authorization verdict, in
    # the order they were asked. It is the read a UI uses to decide which controls
    # to show.
    AuthzCheck() returns (rep: O11yCheckOut)
    # Clones an existing v2-shape dashboard. User and integration
    # dashboards can be cloned; system dashboards are rejected. The clone keeps the
    # source's display name, panels and tags, but gets a freshly generated unique
    # internal name and is always created as an unlocked user dashboard owned by the
    # caller.
    # Callers need the editor role; the runtime's own gate enforces it.
    CloneDashboardV2(req: O11yDashboardIDIn) returns (rep: O11yDashboardOut)
    # Connects a new cloud-integration account for the given
    # provider from its posted config and credentials, answering with the account
    # and the artifact the agent deploys to complete the connection. Admin gate.
    CreateAccount(req: O11yCreateAccountIn) returns (rep: O11yCreateAccountOut)
    # Invites several people to the caller's org in one call,
    # refusing the whole batch when any email repeats. Deprecated alongside
    # createInvite. Admin gate.
    CreateBulkInvite(req: O11yBulkInviteIn) returns (rep: O11yAck)
    # Creates a dashboard in the v2 format that follows the Perses
    # spec and answers with the stored dashboard.
    # Callers need the editor role; the runtime's own gate enforces it.
    CreateDashboardV2(req: O11yDashboardPostable) returns (rep: O11yDashboardOut)
    # Persists the calling user's dashboard-listing state (query,
    # sort, order) as a named, reusable view shared across the org.
    # Callers need the editor role; the runtime's own gate enforces it.
    CreateDashboardView(req: O11yDashboardViewPostable) returns (rep: O11yDashboardViewOut)
    # Creates a planned maintenance window, answering with
    # the stored schedule. Editor gate.
    CreateDowntimeSchedule(req: PostablePlannedMaintenance) returns (rep: O11yDowntimeScheduleOut)
    # Mints an ingestion key for the workspace, answering with
    # the created key. Editor gate.
    CreateIngestionKey(req: PostableIngestionKey) returns (rep: O11yCreatedIngestionKeyOut)
    # Sets a signal limit on an ingestion key, by key id,
    # answering with the created limit. Editor gate.
    CreateIngestionKeyLimit(req: O11yCreateLimitIn) returns (rep: O11yCreatedLimitOut)
    # Invites one person to the caller's org by email, with the role
    # they will hold when they accept. Deprecated in favor of creating users
    # directly; kept because callers still hold it. Admin gate, enforced by the
    # runtime this op relays to.
    CreateInvite(req: O11yInviteIn) returns (rep: O11yInviteOut)
    # Adds a human annotation to a trace or observation,
    # optionally in a review queue.
    # Callers need the editor role; the runtime's own gate enforces it, and it
    # validates the payload and stamps the annotation's author and org.
    CreateLLMAnnotation(req: O11yLLMIngestAnnotation) returns (rep: O11yLLMAnnotationOut)
    # Attaches an eval score or human-feedback signal to a trace or a
    # single observation.
    # Callers need the editor role; the runtime's own gate enforces it, and it
    # validates the payload and stamps the score's author and org.
    CreateLLMScore(req: O11yLLMIngestScore) returns (rep: O11yLLMScoreOut)
    # Creates a volume-control rule for a metric and returns
    # it with its id; a metric that already has a rule is refused.
    CreateMetricReductionRule(req: O11yReductionRuleCreateIn) returns (rep: O11yReductionRuleOut)
    # Writes the pricing-rule batch — the single write
    # endpoint used by both the user and the Zeus sync job. Per-rule match is by id,
    # then sourceId, then insert; an override row is fully preserved when the
    # request omits isOverride, only its synced_at stamped.
    # Callers need the admin role; the runtime's own gate enforces it.
    CreateOrUpdateLLMPricingRules(req: O11yLLMUpdatablePricingRules)
    # Creates the public-sharing config for a dashboard and
    # enables public sharing, answering with the new share's id.
    # Callers need the admin role; the runtime's own gate enforces it.
    CreatePublicDashboard(req: O11yPublicDashboardWriteIn) returns (rep: O11yIdentifiableOut)
    # Creates or regenerates a user's reset-password token: a
    # live token is returned as it is, an expired one is replaced. Admin gate.
    CreateResetPasswordToken(req: O11yUserRef) returns (rep: O11yResetTokenOut)
    # Creates a custom role in the caller's org from a name, an optional
    # description and the transaction groups it grants, answering the new role's id.
    # Names are lowercase letters and hyphens only, and may not start with the
    # reserved managed-role prefix; the runtime refuses anything else.
    CreateRole(req: O11yRoleCreateIn) returns (rep: O11yRoleCreateOut)
    # Creates a route policy, answering with the stored policy. Admin gate.
    CreateRoutePolicy(req: PostableRoutePolicy) returns (rep: O11yRoutePolicyOut)
    # Creates a service account in the caller's org,
    # answering its id. The name — a lowercase letter followed by lowercase
    # letters, digits or hyphens — becomes the account's email local part.
    CreateServiceAccount(req: O11yServiceAccountCreateIn) returns (rep: O11yServiceAccountCreateOut)
    # Mints an API key for a service account and answers
    # the key's id and its secret — the one time the secret is ever shown.
    # ExpiresAt is a unix timestamp in seconds; zero means the key never expires,
    # and a timestamp in the past is refused.
    CreateServiceAccountKey(req: O11yAPIKeyCreateIn) returns (rep: O11yAPIKeyCreateOut)
    # Assigns a role, named by its id, to a service
    # account.
    CreateServiceAccountRole(req: O11yServiceAccountRoleGrantIn)
    # Signs a user in with email and password and
    # answers with the session's token pair. Unauthenticated: this call is how
    # authentication begins.
    CreateSessionByEmailPassword(req: O11yEmailPasswordSessionIn) returns (rep: O11yTokenOut)
    # Adds a mapper to a group: which field context it reads, the
    # move or copy it performs, and whether it is on.
    # Callers need the admin role; the runtime's own gate enforces it.
    CreateSpanMapper(req: O11ySpanMapperCreateIn) returns (rep: O11ySpanMapperOut)
    # Creates a mapping group: the name it is known by, the
    # span and resource attributes whose presence selects a span into it, and
    # whether it is on.
    # Callers need the admin role; the runtime's own gate enforces it.
    CreateSpanMapperGroup(req: PostableSpanMapperGroup) returns (rep: O11ySpanMapperGroupOut)
    # Creates a member of the caller's org in the pending-invite state
    # and mails them their invitation; the answer is the new user's id. Admin gate.
    CreateUser(req: O11yPostableUser) returns (rep: O11yCreatedOut)
    # Releases an email domain and discards its SSO
    # configuration, by id. Admin gate.
    DeleteAuthDomain(req: O11yDomainRef)
    # Removes a notification channel, by id. Admin gate.
    DeleteChannelByID(req: O11yChannelRef)
    # Deletes a v2-shape dashboard along with its tag relations.
    # Locked dashboards are rejected.
    # Callers need the editor role; the runtime's own gate enforces it.
    DeleteDashboardV2(req: O11yDashboardIDIn)
    # Removes a saved view. Saved views are shared org-wide.
    # Deleting a non-existent view refuses with the runtime's not-found.
    # Callers need the editor role; the runtime's own gate enforces it.
    DeleteDashboardView(req: O11yDashboardIDIn)
    # Removes a planned maintenance window, by id. Editor gate.
    DeleteDowntimeScheduleByID(req: O11yDowntimeRef)
    # Removes an ingestion key, by id. Editor gate.
    DeleteIngestionKey(req: O11yIngestionKeyRef)
    # Removes an ingestion key limit, by limit id. Editor
    # gate.
    DeleteIngestionKeyLimit(req: O11yLimitRef)
    # Hard-deletes a pricing rule by id. If the rule was
    # auto-synced, the next sync cycle recreates it.
    # Callers need the admin role; the runtime's own gate enforces it.
    DeleteLLMPricingRule(req: O11yLLMPricingRuleRef)
    # Hard-deletes a score by id.
    # Callers need the editor role; the runtime's own gate enforces it.
    DeleteLLMScore(req: O11yLLMScoreRef)
    # Deletes a volume-control rule by its id.
    DeleteMetricReductionRuleByID(req: O11yReductionRuleRef)
    # Deletes the public-sharing config and disables public
    # sharing of a dashboard.
    # Callers need the admin role; the runtime's own gate enforces it.
    DeletePublicDashboard(req: O11yDashboardIDIn)
    # Deletes a custom role. A role that still has user or
    # service-account assignees, or an auth-domain mapping, is refused; managed
    # roles cannot be deleted.
    DeleteRole(req: O11yRoleDeleteIn)
    # Removes a route policy, by id. Admin gate.
    DeleteRoutePolicyByID(req: O11yRoutePolicyRef)
    # Removes an alert rule, by id. Editor gate.
    DeleteRuleByID(req: O11yRuleRef)
    # Deletes a service account and revokes every key it
    # holds.
    DeleteServiceAccount(req: O11yServiceAccountDeleteIn)
    # Removes a role from a service account.
    DeleteServiceAccountRole(req: O11yServiceAccountRoleRevokeIn)
    # Signs the calling session out, invalidating its tokens. The
    # access token on the call names the session to end.
    DeleteSession()
    # Deletes one mapper from a group.
    # Callers need the admin role; the runtime's own gate enforces it.
    DeleteSpanMapper(req: O11ySpanMapperRef)
    # Deletes a mapping group and every mapper under it.
    # Callers need the admin role; the runtime's own gate enforces it.
    DeleteSpanMapperGroup(req: O11ySpanMapperGroupRef)
    # Deletes a funnel. The answer carries no data — the runtime
    # acknowledges with the success envelope alone, which is what this Out says.
    # Callers need the editor role; the runtime's own gate enforces it.
    DeleteTraceFunnel(req: O11yFunnelRef) returns (rep: O11yFunnelDeleteOut)
    # Removes one org member, by user id. Admin gate.
    DeleteUser(req: O11yUserRef)
    # Removes one org member, by user id. The same operation
    # as deleteUser on the legacy singular path. Admin gate.
    DeleteUserDeprecated(req: O11yUserRef)
    # Tears down a connected account for the given provider, by
    # id. Admin gate.
    DisconnectAccount(req: O11yAccountRef)
    # Starts the forgotten-password flow: the named user is mailed
    # a reset link. Unauthenticated by design, and deliberately quiet about
    # whether the address exists.
    ForgotPassword(req: O11yForgotPasswordIn)
    # Lists the org's route policies. Viewer gate.
    GetAllRoutePolicies() returns (rep: O11yRoutePoliciesOut)
    # Returns one notification channel, by id. Viewer gate.
    GetChannelByID(req: O11yChannelRef) returns (rep: O11yChannelOut)
    # Returns the credentials the connecting agent needs
    # to establish the cloud integration, for the given cloud provider. Admin gate.
    GetConnectionCredentials(req: O11yCloudProviderRef) returns (rep: O11yCredentialsOut)
    # Returns a v2-shape dashboard.
    # Callers need the viewer role; the runtime's own gate enforces it.
    GetDashboardV2(req: O11yDashboardIDIn) returns (rep: O11yDashboardOut)
    # Returns one planned maintenance window, by id. Viewer gate.
    GetDowntimeScheduleByID(req: O11yDowntimeRef) returns (rep: O11yDowntimeScheduleOut)
    # Returns the deployment's host info from Zeus. Viewer gate.
    GetHosts() returns (rep: O11yGettableHostOut)
    # Lists the workspace's ingestion keys, paginated. Editor
    # gate.
    GetIngestionKeys(req: O11yIngestionKeysIn) returns (rep: O11yIngestionKeysOut)
    # Reports whether the integration's logs and
    # metrics have been received over the lookback window, so the console can show
    # a live connection state. An integration that is not installed answers with an
    # empty status rather than an error. Viewer gate.
    GetIntegrationConnectionStatus(req: O11yConnectionStatusIn) returns (rep: O11yConnectionStatusOut)
    # Returns a single LLM pricing rule by id.
    # Callers need the viewer role; the runtime's own gate enforces it.
    GetLLMPricingRule(req: O11yLLMPricingRuleRef) returns (rep: O11yLLMPricingRuleOut)
    # Returns a single score by id.
    # Callers need the viewer role; the runtime's own gate enforces it.
    GetLLMScore(req: O11yLLMScoreRef) returns (rep: O11yLLMScoreOut)
    # Lists the alert rules that reference a metric.
    GetMetricAlerts(req: O11yMetricNameIn) returns (rep: O11yMetricAlertsOut)
    # Returns one metric's attribute keys, each with its unique
    # values and their count.
    GetMetricAttributes(req: O11yMetricAttributesIn) returns (rep: O11yMetricAttributesOut)
    # Lists the dashboard panels that reference a metric.
    GetMetricDashboardsV2(req: O11yMetricNameIn) returns (rep: O11yMetricDashboardsOut)
    # Returns one metric's headline numbers: data points, total
    # and active time series, and when it was last received.
    GetMetricHighlights(req: O11yMetricNameIn) returns (rep: O11yMetricHighlightsOut)
    # Returns one metric's metadata: description, type, unit,
    # temporality and monotonicity.
    GetMetricMetadata(req: O11yMetricNameIn) returns (rep: O11yMetricMetadataOut)
    # Returns one volume-control rule by its id.
    GetMetricReductionRuleByID(req: O11yReductionRuleRef) returns (rep: O11yReductionRuleOut)
    # Returns total ingested vs retained series and samples and
    # the estimated monthly savings across all volume-control rules.
    GetMetricReductionRuleStats() returns (rep: O11yReductionStatsOut)
    # Reports whether any non-O11y metrics have been ingested —
    # the lightweight check onboarding polls.
    GetMetricsOnboardingStatus() returns (rep: O11yMetricOnboardingOut)
    # Lists metrics with their sample and time-series counts for a
    # time range — the volume view of the metrics explorer, pageable and sortable.
    GetMetricsStats(req: O11yMetricStatsIn) returns (rep: O11yMetricStatsOut)
    # Returns the proportional distribution of metrics by sample
    # count or time-series count, as the entries of a treemap.
    GetMetricsTreemap(req: O11yMetricTreemapIn) returns (rep: O11yMetricTreemapOut)
    # Returns the caller's own organization. Admin gate.
    GetMyOrganization() returns (rep: O11yOrganizationOut)
    # Returns the calling service account itself, with the
    # roles it holds — the self-inspection read for a key-authenticated caller.
    GetMyServiceAccount() returns (rep: O11yServiceAccountOut)
    # Returns the calling user together with every role they hold. Open
    # to any authenticated caller.
    GetMyUser() returns (rep: O11yUserWithRolesOut)
    # Returns the calling user with their single legacy role.
    # Deprecated in favor of getMyUser. Open to any authenticated caller.
    GetMyUserDeprecated() returns (rep: O11yDeprecatedUserOut)
    # Returns the public-sharing config for a dashboard.
    # Callers need the admin role; the runtime's own gate enforces it.
    GetPublicDashboard(req: O11yDashboardIDIn) returns (rep: O11yPublicDashboardOut)
    # Returns the sanitized dashboard data for public access —
    # the read a shared dashboard's public page makes.
    # Anonymous, scoped to the public dashboard's read scope; the runtime's own gate
    # enforces it.
    GetPublicDashboardData(req: O11yDashboardIDIn) returns (rep: O11yPublicDashboardDataOut)
    # Returns the query-range result for one widget
    # of a public dashboard. When the share fixes its own time range the caller's
    # startTime/endTime are ignored; otherwise they bound the window as millisecond
    # epochs.
    # Anonymous, scoped to the public dashboard's read scope; the runtime's own gate
    # enforces it.
    GetPublicDashboardWidgetQueryRange(req: O11yWidgetQueryRangeIn) returns (rep: O11yWidgetQueryRangeOut)
    # Returns the org's quick filters for every signal — the
    # attribute shortlists its explorers offer as one-click filters. Viewer gate.
    GetQuickFilters() returns (rep: O11yQuickFiltersOut)
    # Returns the reset-password token a user already has; absent
    # one, the answer is a not-found rather than a fresh token. Admin gate.
    GetResetPasswordToken(req: O11yUserRef) returns (rep: O11yResetTokenOut)
    # Returns a user's password-reset token, creating one
    # if none is live. Deprecated in favor of the reset_password_tokens pair,
    # which separates reading from minting. Admin gate.
    GetResetPasswordTokenDeprecated(req: O11yUserRef) returns (rep: O11yResetTokenOut)
    # Returns one role with the transaction groups it grants.
    GetRole(req: O11yRoleGetIn) returns (rep: O11yRoleOut)
    # Returns every role one org member holds, by user id. Admin
    # gate.
    GetRolesByUserID(req: O11yUserRef) returns (rep: O11yRolesOut)
    # Returns one route policy, by id. Viewer gate.
    GetRoutePolicyByID(req: O11yRoutePolicyRef) returns (rep: O11yRoutePolicyOut)
    # Returns the distinct values a given label key has
    # taken across a rule's history entries. Viewer gate.
    GetRuleHistoryFilterValues(req: O11yRuleHistoryFilterValuesIn) returns (rep: O11yRuleHistoryFilterValuesOut)
    # Returns the overall firing/inactive intervals for
    # a rule over the selected range. Viewer gate.
    GetRuleHistoryOverallStatus(req: O11yRuleHistoryBaseIn) returns (rep: O11yRuleHistoryOverallStatusOut)
    # Returns one service account with the roles it holds.
    GetServiceAccount(req: O11yServiceAccountGetIn) returns (rep: O11yServiceAccountOut)
    # Lists the roles a service account holds.
    GetServiceAccountRoles(req: O11yServiceAccountRolesIn) returns (rep: O11yServiceAccountRolesOut)
    # Tells a sign-in page what an email address can do: which
    # orgs the address belongs to and, per org, which password and SSO routes are
    # open to it. Unauthenticated: it runs before any session exists.
    GetSessionContext(req: O11ySessionContextIn) returns (rep: O11ySessionContextOut)
    # Returns the org's quick filters for one signal — traces,
    # logs, metrics, exceptions or api_monitoring. Viewer gate.
    GetSignalFilters(req: O11ySignalRef) returns (rep: O11ySignalFiltersOut)
    # Returns the trace field catalog: the span fields already selected
    # as indexed columns, and the interesting ones seen in the data that could be.
    # Callers need the viewer role; the runtime's own gate enforces it.
    GetTraceFields() returns (rep: O11yFieldCatalogOut)
    # Returns one org member together with every role they hold, by user
    # id. Admin gate.
    GetUser(req: O11yUserRef) returns (rep: O11yUserWithRolesOut)
    # Returns one org member with their single legacy role, by
    # user id. Admins may read anyone; a non-admin only themselves (the runtime's
    # self-access gate).
    GetUserDeprecated(req: O11yUserRef) returns (rep: O11yDeprecatedUserOut)
    # Returns every org member holding a role, by role id. Admin
    # gate.
    GetUsersByRoleID(req: O11yRoleUsersIn) returns (rep: O11yUsersOut)
    # Lists the services metadata for one connected
    # account of the given provider, by account id. Admin gate.
    ListAccountServicesMetadata(req: O11yAccountRef) returns (rep: O11yServicesMetadataOut)
    # Lists the org's notification channels. Viewer gate.
    ListChannels() returns (rep: O11yChannelsOut)
    # Returns every saved view in the calling user's org. Saved
    # views are shared org-wide.
    # Callers need the viewer role; the runtime's own gate enforces it.
    ListDashboardViews() returns (rep: O11yDashboardViewListOut)
    # Is dashboardListV2 personalized for the calling user:
    # each dashboard carries the caller's pinned state, and pinned dashboards float to
    # the top of the requested ordering. Supports the same filter DSL, sort, order and
    # pagination.
    # Callers need the viewer role; the runtime's own gate enforces it.
    ListDashboardsForUserV2(req: O11yDashboardListParams) returns (rep: O11yDashboardListForUserOut)
    # Returns a page of v2-shape dashboards for the org. This is the
    # pure, user-independent list — it carries no pin state; use
    # dashboardListForUserV2 for the personalized, pin-aware list. Supports a filter
    # DSL (query), sort (updated_at/created_at/name), order (asc/desc), and
    # offset-based pagination (limit/offset).
    # Callers need the viewer role; the runtime's own gate enforces it.
    ListDashboardsV2(req: O11yDashboardListParams) returns (rep: O11yDashboardListOut)
    # Lists all planned maintenance windows, optionally
    # narrowed to the active ones or the recurring ones. Viewer gate.
    ListDowntimeSchedules(req: O11yListDowntimeSchedulesIn) returns (rep: O11yDowntimeSchedulesOut)
    # Lists the available integrations and whether each is
    # installed in the caller's org, optionally narrowed to installed or
    # not-installed. Viewer gate.
    ListIntegrations(req: O11yListIntegrationsIn) returns (rep: O11yIntegrationsListOut)
    # Lists human annotations on traces and observations,
    # optionally scoped to one review queue.
    # Callers need the viewer role; the runtime's own gate enforces it.
    ListLLMAnnotations(req: O11yLLMAnnotationsQuery) returns (rep: O11yLLMAnnotationsOut)
    # Lists gen_ai spans as LLM observations — each an LLM call with
    # its model, token counts, cost and latency projected from gen_ai.* attributes,
    # newest first, over the query window.
    # Callers need the viewer role; the runtime's own gate enforces it, and scopes
    # the read to the caller's validated tenant.
    ListLLMObservations(req: O11yLLMViewQuery) returns (rep: O11yLLMObservationsOut)
    # Returns the LLM pricing rules for the caller's org, with
    # pagination and an optional search and override filter.
    # Callers need the viewer role; the runtime's own gate enforces it.
    ListLLMPricingRules(req: O11yLLMPricingRulesQuery) returns (rep: O11yLLMPricingRulesOut)
    # Lists eval scores and human-feedback signals attached to traces
    # and observations, newest first.
    # Callers need the viewer role; the runtime's own gate enforces it.
    ListLLMScores(req: O11yLLMScoresQuery) returns (rep: O11yLLMScoresOut)
    # Lists conversations — gen_ai spans grouped by session.id, with
    # their trace and observation counts, tokens and cost.
    # Callers need the viewer role; the runtime's own gate enforces it.
    ListLLMSessions(req: O11yLLMViewQuery) returns (rep: O11yLLMSessionsOut)
    # Lists LLM traces — gen_ai spans grouped by trace_id, with cost,
    # tokens and latency rolled up across each trace.
    # Callers need the viewer role; the runtime's own gate enforces it.
    ListLLMTraces(req: O11yLLMViewQuery) returns (rep: O11yLLMTracesOut)
    # Lists end users — gen_ai spans grouped by user.id, with their
    # session, trace and observation counts, tokens and cost.
    # Callers need the viewer role; the runtime's own gate enforces it.
    ListLLMUsers(req: O11yLLMViewQuery) returns (rep: O11yLLMUsersOut)
    # Lists the org's metric volume-control (label reduction)
    # rules, pageable and sortable by name, volume or recency.
    ListMetricReductionRules(req: O11yReductionRuleListIn) returns (rep: O11yReductionRuleListOut)
    # Lists the distinct metric names seen in a time range, each with
    # its description, type, unit, temporality and monotonicity.
    ListMetrics(req: O11yMetricListIn) returns (rep: O11yMetricListOut)
    # Lists every role in the caller's org — the managed ones the
    # platform seeds and the custom ones its admins created.
    ListRoles() returns (rep: O11yRolesOut)
    # Lists a service account's API keys — metadata only,
    # never the secrets.
    ListServiceAccountKeys(req: O11yAPIKeysIn) returns (rep: O11yAPIKeysOut)
    # Lists the caller's org's service accounts.
    ListServiceAccounts() returns (rep: O11yServiceAccountsOut)
    # Lists the services the given provider can collect from,
    # optionally scoped to one cloud integration. Admin gate.
    ListServicesMetadata(req: O11yListServicesMetadataIn) returns (rep: O11yServicesMetadataOut)
    # Lists the caller's org's mapping groups, optionally only the
    # enabled ones.
    # Callers need the viewer role; the runtime's own gate enforces it.
    ListSpanMapperGroups(req: O11ySpanMapperGroupsIn) returns (rep: O11ySpanMapperGroupsOut)
    # Lists the mappers belonging to one group, in the order they are
    # applied.
    # Callers need the viewer role; the runtime's own gate enforces it.
    ListSpanMappers(req: O11ySpanMapperGroupRef) returns (rep: O11ySpanMappersOut)
    # Lists the caller's org members. Admin gate.
    ListUsers() returns (rep: O11yUsersOut)
    # Lists the org's members with their single legacy role.
    # Deprecated in favor of listUsers, which answers without the role. Admin gate.
    ListUsersDeprecated() returns (rep: O11yDeprecatedUsersOut)
    # Locks a v2-shape dashboard. Only the dashboard's creator or an
    # org admin may lock or unlock.
    # Callers need the editor role; the runtime's own gate enforces it.
    LockDashboardV2(req: O11yDashboardIDIn)
    # Applies an RFC 6902 JSON Patch to a v2-shape dashboard. The
    # patch is applied against the postable view (metadata, spec, tags), so individual
    # panels, queries, variables, layouts or tags can be updated without re-sending the
    # rest. Apply is lenient — remove on a missing path is a no-op and add creates any
    # missing parent objects — and the result is still validated. Locked dashboards are
    # rejected. The request body is the bare JSON Patch operations array.
    # Callers need the editor role; the runtime's own gate enforces it.
    PatchDashboardV2(req: O11yDashboardPatchIn) returns (rep: O11yDashboardOut)
    # Pins a dashboard for the calling user. A user can pin at most ten
    # dashboards; pinning at the limit refuses with the runtime's conflict. Re-pinning
    # an already-pinned dashboard is a no-op success. Pinning mutates only the caller's
    # pin list, not the dashboard, so a viewer may pin what a viewer may read.
    # Callers need the viewer role; the runtime's own gate enforces it.
    PinDashboardV2(req: O11yDashboardIDIn)
    # Estimates the series reduction and the dashboards and
    # alerts a candidate volume-control rule would touch, without persisting it.
    PreviewMetricReductionRule(req: O11yReductionRulePreviewIn) returns (rep: O11yReductionRulePreviewOut)
    # Records the deployment's host in Zeus, overwriting any prior one.
    # Admin gate.
    PutHost(req: PostableHost)
    # Records the deployment's profile in Zeus — how the team uses
    # observability today and what they plan — overwriting any prior one. Admin
    # gate.
    PutProfile(req: PostableProfile)
    # Takes a role away from one org member, by user id and role
    # id — someone else, never the caller. Admin gate.
    RemoveUserRoleByUserIDAndRoleID(req: O11yUserRoleRef)
    # Sets a new password for whoever the reset token was minted
    # for, consuming the token. Unauthenticated: the token is the proof.
    ResetPassword(req: O11yResetPasswordIn)
    # Revokes an API key. Revocation is immediate and
    # permanent.
    RevokeServiceAccountKey(req: O11yAPIKeyRevokeIn)
    # Exchanges a refresh token for a fresh token pair, retiring the
    # old pair. The access token being rotated identifies the session.
    RotateSession(req: O11yRotateSessionIn) returns (rep: O11yTokenOut)
    # Lists the workspace's ingestion keys whose name matches
    # the search, paginated. Editor gate.
    SearchIngestionKeys(req: O11ySearchIngestionKeysIn) returns (rep: O11yIngestionKeysOut)
    # Returns one trace's spans as a column/row table, optionally
    # centred on a span and walked a fixed number of levels up and down from it —
    # the read the trace explorer opens a trace with.
    # Callers need the viewer role; the runtime's own gate enforces it.
    SearchTraces(req: O11yTraceSpansIn)
    # Assigns a role, by role name, to one org member — someone else,
    # never the caller. Admin gate.
    SetRoleByUserID(req: O11ySetRoleIn) returns (rep: O11yAck)
    # Removes an integration from the caller's org by id.
    # Viewer gate.
    UninstallIntegration(req: UninstallIntegrationRequest) returns (rep: O11yIntegrationAck)
    # Unlocks a v2-shape dashboard. Only the dashboard's creator or
    # an org admin may lock or unlock.
    # Callers need the editor role; the runtime's own gate enforces it.
    UnlockDashboardV2(req: O11yDashboardIDIn)
    # Removes the caller's pin for a dashboard. Idempotent —
    # unpinning a dashboard that was not pinned still succeeds.
    # Callers need the viewer role; the runtime's own gate enforces it.
    UnpinDashboardV2(req: O11yDashboardIDIn)
    # Changes a connected account's configuration for the given
    # provider, by id. Admin gate.
    UpdateAccount(req: O11yUpdateAccountIn)
    # Updates a v2-shape dashboard's metadata, spec and tag set.
    # The name is immutable and locked dashboards are rejected.
    # Callers need the editor role; the runtime's own gate enforces it.
    UpdateDashboardV2(req: O11yDashboardUpdateIn) returns (rep: O11yDashboardOut)
    # Replaces a saved view's name and data. Saved views are shared
    # org-wide.
    # Callers need the editor role; the runtime's own gate enforces it.
    UpdateDashboardView(req: O11yDashboardViewUpdateIn) returns (rep: O11yDashboardViewOut)
    # Replaces a planned maintenance window, by id. Editor gate.
    UpdateDowntimeScheduleByID(req: O11yDowntimeUpdateIn)
    # Changes an ingestion key, by id. Editor gate.
    UpdateIngestionKey(req: O11yUpdateIngestionKeyIn)
    # Changes an ingestion key limit, by limit id. Editor
    # gate.
    UpdateIngestionKeyLimit(req: O11yUpdateLimitIn)
    # Updates one metric's metadata — description, type, unit,
    # temporality, monotonicity — and answers with the bare success envelope.
    UpdateMetricMetadata(req: O11yMetricMetadataSaveIn) returns (rep: O11yMetricAckOut)
    # Updates the match type and labels of a volume-control rule
    # by its id; the metric name is immutable.
    UpdateMetricReductionRuleByID(req: O11yReductionRuleSaveIn) returns (rep: O11yReductionRuleOut)
    # Rewrites the caller's own organization record — display name,
    # name, alias — always addressed as "me", never by id. Admin gate.
    UpdateMyOrganization(req: O11yOrganization)
    # Replaces the calling user's password, refusing when the old
    # one does not match. Open to any authenticated caller.
    UpdateMyPassword(req: O11yChangePasswordIn)
    # Renames the calling service account.
    UpdateMyServiceAccount(req: O11yMyServiceAccountUpdateIn)
    # Renames the calling user. Open to any authenticated caller.
    UpdateMyUserV2(req: O11yUpdatableUser)
    # Updates the public-sharing config for a dashboard.
    # Callers need the admin role; the runtime's own gate enforces it.
    UpdatePublicDashboard(req: O11yPublicDashboardWriteIn)
    # Replaces the org's quick filters for one signal with the
    # attribute list given. Admin gate.
    UpdateQuickFilters(req: O11yUpdatableQuickFilters)
    # Replaces a custom role's description and transaction groups.
    # Both fields are mandatory — send an empty string or an empty array to clear
    # one — and managed roles cannot be edited.
    UpdateRole(req: O11yRoleUpdateIn)
    # Replaces a route policy, by id, answering with the stored
    # policy. Admin gate.
    UpdateRoutePolicy(req: O11yRoutePolicyUpdateIn) returns (rep: O11yRoutePolicyOut)
    # Renames a service account.
    UpdateServiceAccount(req: O11yServiceAccountUpdateIn)
    # Renames an API key or moves its expiry.
    UpdateServiceAccountKey(req: O11yAPIKeyUpdateIn)
    # Changes a mapper's field context, config or enabled state.
    # Every field is optional and only the ones sent are applied.
    # Callers need the admin role; the runtime's own gate enforces it.
    UpdateSpanMapper(req: O11ySpanMapperUpdateIn)
    # Changes a group's name, condition or enabled state.
    # Every field is optional and only the ones sent are applied.
    # Callers need the admin role; the runtime's own gate enforces it.
    UpdateSpanMapperGroup(req: O11ySpanMapperGroupUpdateIn)
    # Changes how one span field is stored — selects or deselects
    # it as a materialized column and tunes its index — and echoes the setting back.
    # Callers need the editor role; the runtime's own gate enforces it.
    UpdateTraceField(req: O11yFieldSetting) returns (rep: O11yFieldSetting)
    # Renames one org member, by user id — someone else, never the
    # caller, who renames themselves through updateMyUser. Admin gate.
    UpdateUser(req: O11yUserUpdate)
    # Renames one org member and may move their legacy role,
    # answering with the updated record. Admins may update anyone; a non-admin
    # only themselves (the runtime's self-access gate).
    UpdateUserDeprecated(req: O11yDeprecatedUserUpdate) returns (rep: O11yDeprecatedUserOut)
    # Checks that a reset-password token exists and has not
    # expired, without consuming it. Unauthenticated: the token is the proof.
    VerifyResetPasswordToken(req: O11yResetTokenRef)
    # Deletes one saved explorer view by id.
    # Callers need the editor role; the runtime's own gate enforces it.
    delete_o11y_explorer_views_by_viewid(req: O11ySavedViewRef) returns (rep: O11ySavedViewDeleteOut)
    # Removes one review queue and every item in it. A queue
    # id belonging to another org answers the same 404 an unknown id does, so a
    # probe learns nothing about what exists.
    delete_o11y_reviews_by_id(req: annQueueRef) returns (rep: annQueueDeleted)
    # Deletes one Sentry project of the caller's org. Its DSN
    # stops resolving immediately, so ingest for that id fails closed exactly as an
    # unknown project does; retained events are not touched. Answers 204.
    # Callers need the editor role; the runtime's own gate enforces it.
    delete_o11y_sentinel_projects_by_id(req: O11ySentryProjectRef)
    # Lists the attributes usable as an aggregate target for
    # the given telemetry and operator — what a filter builder offers after the
    # aggregation is chosen.
    # Callers need the viewer role; the runtime's own gate enforces it.
    get_o11y_autocomplete_aggregate_attributes(req: O11yAggregateAttributesIn) returns (rep: O11yAggregateAttributesOut)
    # Lists the attribute keys available for filtering the given
    # telemetry, each with its data type and whether it is a materialized column.
    # Callers need the viewer role; the runtime's own gate enforces it.
    get_o11y_autocomplete_attribute_keys(req: O11yAttributeKeysIn) returns (rep: O11yAttributeKeysOut)
    # Reports how much of the Hanzo fleet is up — the current
    # per-service inventory plus an up-versus-reporting trend across the window.
    # Both come from the fleet prober's own measurements: every service is asked its
    # health URL every 30 seconds, so a service is listed as down because it did not
    # answer, never because something failed to collect it. PLATFORM SUDO ONLY —
    # this is the whole fleet's inventory, not tenant data, so every customer is
    # 403. An unreachable telemetry store answers 503 rather than an empty trend,
    # because a board of zeroes and a fleet that is down look identical.
    get_o11y_availability(req: availabilityIn) returns (rep: availabilityResponse)
    # Lists the metric attribute keys Kubernetes clusters
    # report, for building cluster filters.
    get_o11y_clusters_attribute_keys(req: O11yInfraAttributeKeysIn) returns (rep: O11yInfraAttributeKeysOut)
    # Lists the metric attribute keys Kubernetes daemonsets
    # report, for building daemonset filters.
    get_o11y_daemonsets_attribute_keys(req: O11yInfraAttributeKeysIn) returns (rep: O11yInfraAttributeKeysOut)
    # Lists the metric attribute keys Kubernetes
    # deployments report, for building deployment filters.
    get_o11y_deployments_attribute_keys(req: O11yInfraAttributeKeysIn) returns (rep: O11yInfraAttributeKeysOut)
    # Lists the storage disks the datastore reports, with their names and
    # types.
    # Callers need the viewer role; the runtime's own gate enforces it.
    get_o11y_disks()
    # Returns one exception instance and the span it happened on,
    # by its error id within a group at a timestamp.
    # Callers need the viewer role; the runtime's own gate enforces it.
    get_o11y_errorfromerrorid(req: O11yErrorLookupIn) returns (rep: O11yErrorWithSpan)
    # Returns the representative exception instance of a group at a
    # timestamp, and the span it happened on.
    # Callers need the viewer role; the runtime's own gate enforces it.
    get_o11y_errorfromgroupid(req: O11yErrorLookupIn) returns (rep: O11yErrorWithSpan)
    # Lists the caller's org's grouped error issues (by
    # fingerprint) with status, level, counts and first/last-seen.
    # Callers need the viewer role; the runtime's own gate enforces it.
    get_o11y_errortracking_issues(req: O11yErrorIssuesIn) returns (rep: O11yErrorIssuesOut)
    # Returns the values one telemetry field has taken — string, bool,
    # number and related values — and whether the value list is complete.
    # Callers need the viewer role; the runtime's own gate enforces it.
    get_o11y_fields_values(req: O11yFieldValuesIn) returns (rep: O11yFieldValuesOut)
    # Returns the deployment's global configuration: its public
    # endpoints and which identity providers are enabled. Open by design — the
    # sign-in page reads it before anyone is signed in.
    # Open by design; the runtime's own gate is OpenAccess.
    get_o11y_global_config() returns (rep: O11yGlobalConfigOut)
    # Reports service health. With live set, the datastore connection is
    # checked too and an unhealthy store refuses with 503.
    # Open by design; the runtime's own gate is OpenAccess.
    get_o11y_health(req: O11yHealthIn) returns (rep: O11yHealthOut)
    # Lists the metric attribute keys hosts report, for building
    # host filters — each with its data type and whether it is a materialized
    # column.
    get_o11y_hosts_attribute_keys(req: O11yInfraAttributeKeysIn) returns (rep: O11yInfraAttributeKeysOut)
    # Reports whether the metrics and attributes an infra-monitoring
    # section needs are being received — for each collector receiver or processor
    # involved, what is present and what is missing, with a user-facing message
    # and a docs link per missing piece. Ready is true only when nothing is
    # missing.
    get_o11y_infra_monitoring_checks(req: O11yInfraChecksIn) returns (rep: O11yInfraChecksOut)
    # Reports how far Kubernetes infra onboarding has progressed:
    # which metric families have arrived and, per pod, which required metadata
    # labels are present.
    # Callers need the viewer role; the runtime's own gate enforces it.
    get_o11y_infra_onboarding_k8s_status() returns (rep: O11yOnboardingOut)
    # Lists the metric attribute keys Kubernetes jobs report, for
    # building job filters.
    get_o11y_jobs_attribute_keys(req: O11yInfraAttributeKeysIn) returns (rep: O11yInfraAttributeKeysOut)
    # Returns the log field catalog: the fields already selected as
    # indexed columns, and the interesting ones seen in the data that could be.
    # Callers need the viewer role; the runtime's own gate enforces it.
    get_o11y_logs_fields() returns (rep: O11yFieldCatalogOut)
    # Lists the log body paths already promoted or indexed, with the
    # indexes each carries.
    # Callers need the viewer role; the runtime's own gate enforces it.
    get_o11y_logs_promote_paths() returns (rep: O11yLogPromotedOut)
    # Serves the OLDER /metric/metric_metadata route. It is
    # NOT the same op as metrics.go's metricMetadata (/metrics/metadata): different
    # path, different input (this one also scopes by service). Two slices named one
    # Go function for two routes; the route is the identity, so the name follows it.
    # Renamed rather than merged — collapsing them would silently drop the service
    # scope this one accepts.
    # It returns one metric's metadata — its type, unit, description,
    # temporality, monotonicity and histogram buckets — optionally scoped to the
    # metric as one service reports it.
    # Callers need the viewer role; the runtime's own gate enforces it.
    get_o11y_metric_metric_metadata(req: O11yMetricMetadataIn) returns (rep: O11yMetricMetadataOut)
    # Lists the metric attribute keys Kubernetes namespaces
    # report, for building namespace filters.
    get_o11y_namespaces_attribute_keys(req: O11yInfraAttributeKeysIn) returns (rep: O11yInfraAttributeKeysOut)
    # Returns the ids of the exception instances immediately after
    # and before a given one within its group — the paging cursor the error detail
    # view walks.
    # Callers need the viewer role; the runtime's own gate enforces it.
    get_o11y_nextpreverrorids(req: O11yErrorLookupIn) returns (rep: O11yNextPrevErrorIDs)
    # Lists the metric attribute keys Kubernetes nodes report,
    # for building node filters.
    get_o11y_nodes_attribute_keys(req: O11yInfraAttributeKeysIn) returns (rep: O11yInfraAttributeKeysOut)
    # Lists the metric attribute keys Kubernetes pods report, for
    # building pod filters.
    get_o11y_pods_attribute_keys(req: O11yInfraAttributeKeysIn) returns (rep: O11yInfraAttributeKeysOut)
    # Lists the metric attribute keys processes report, for
    # building process filters.
    get_o11y_processes_attribute_keys(req: O11yInfraAttributeKeysIn) returns (rep: O11yInfraAttributeKeysOut)
    # Returns one product's RED series — request rate, errors, p50
    # and p95 latency — for the caller's org, plus that org's LLM usage rollup over
    # the same window. The series come from org-tagged request spans, so a tenant
    # only ever aggregates its own traffic; a validated platform SuperAdmin sees the
    # whole product's RED, while usage stays the caller's own org either way. A
    # well-formed product with no backing workload answers empty series; a malformed
    # slug is a 400.
    get_o11y_product_metrics(req: metricsIn) returns (rep: metricsResponse)
    # Lists the metric attribute keys persistent volume claims
    # report, for building volume filters.
    get_o11y_pvcs_attribute_keys(req: O11yInfraAttributeKeysIn) returns (rep: O11yInfraAttributeKeysOut)
    # Evaluates one instant PromQL query against the org's metrics and
    # returns the result at a single point in time.
    # The result is polymorphic by PromQL's own contract — a matrix, vector,
    # scalar or string, discriminated by resultType — so it is carried verbatim
    # rather than forced into one of its shapes.
    # Callers need the viewer role; the runtime's own gate enforces it.
    get_o11y_query(req: O11yPromQueryIn) returns (rep: O11yPromQueryOut)
    # Runs a Prometheus-style range query over metrics — the
    # legacy read that predates the v5 querier — and returns the matrix, vector or
    # scalar the query resolved to.
    # Callers need the viewer role; the runtime's own gate enforces it.
    get_o11y_query_range(req: O11yMetricsQueryRangeIn) returns (rep: O11yMetricsQueryRangeOut)
    # Returns a page of the caller org's human-review queues,
    # newest first, narrowed to the caller's project. Another org's queues are never
    # visible.
    get_o11y_reviews(req: annPage) returns (rep: annQueueList)
    # Returns one review queue with its pending and completed
    # counts and its first page of items. A queue id belonging to another org is a
    # 404, never a cross-tenant read.
    get_o11y_reviews_by_id(req: annQueueRef) returns (rep: annQueueDetailView)
    # Returns a page of one review queue's items, newest
    # first, optionally filtered to PENDING or COMPLETED. A queue id belonging to
    # another org is a 404, never a cross-tenant list.
    get_o11y_reviews_by_id_items(req: listItemsIn) returns (rep: annItemList)
    # Lists the caller's org's grouped error issues, optionally
    # narrowed to one project and one time window, and filtered by status, level,
    # environment, service, a free-text query and a sort.
    # Callers need the viewer role; the runtime's own gate enforces it.
    get_o11y_sentinel_issues(req: O11ySentryIssuesIn) returns (rep: O11yErrorIssuesOut)
    # Lists the caller's org's Sentry projects, each with its
    # freshly-derived DSN.
    # Callers need the viewer role; the runtime's own gate enforces it.
    get_o11y_sentinel_projects() returns (rep: O11ySentryProjectsOut)
    # Returns one Sentry project of the caller's org, DSN included.
    # Callers need the viewer role; the runtime's own gate enforces it.
    get_o11y_sentinel_projects_by_id(req: O11ySentryProjectRef) returns (rep: O11ySentryProjectOut)
    # Returns a project's event-rate timeseries: one bucket per interval over
    # the requested period, counting the events in it.
    get_o11y_sentinel_stats(req: O11yStatsIn) returns (rep: O11yStatsOut)
    # Lists the traces a project's captured errors reference, each with how
    # many errors landed on it, when they started and stopped, and the latest
    # message seen — the entry point for "which requests are failing".
    get_o11y_sentinel_traces(req: O11yTracesIn) returns (rep: O11yTracesOut)
    # Lists the name of every service the trace store holds, with no
    # window applied — the complete catalog, for pickers and autocomplete.
    get_o11y_services_list()
    # Returns apdex settings for the named services.
    # Callers need the viewer role; the runtime's own gate enforces it.
    get_o11y_settings_apdex(req: O11yApdexIn) returns (rep: O11yApdexOut)
    # Returns the org's current retention policy: default TTL, custom
    # per-label rules, and cold-storage settings where configured.
    # Callers need the viewer role; the runtime's own gate enforces it.
    get_o11y_settings_ttl() returns (rep: O11yRetentionOut)
    # Lists the metric attribute keys Kubernetes
    # statefulsets report, for building statefulset filters.
    get_o11y_statefulsets_attribute_keys(req: O11yInfraAttributeKeysIn) returns (rep: O11yInfraAttributeKeysOut)
    # Reports whether a product's service is live: an in-cluster
    # health probe with its measured latency, fused with the per-replica up
    # inventory. Infra health is not tenant-partitioned — a service is up or down
    # for everyone — so any validated caller is served, but an unvalidated one is
    # refused. A product with no backing workload answers down/unknown-service
    # without probing anything; a malformed slug is a 400.
    get_o11y_status(req: statusIn) returns (rep: statusResult)
    # Reports whether the platform is up. It returns the public status
    # document: the incidents currently open against Hanzo's own services, derived
    # from the fleet health probes, plus the address of the human status page. No
    # authentication is required and no tenant data is involved — the answer is the
    # same for every caller.
    # A service that fails its health probe becomes one incident naming that service.
    # When the availability source itself cannot be read the endpoint answers 503
    # rather than an empty incident list, because "we cannot tell" and "everything is
    # fine" are different answers and only one of them is true.
    get_o11y_summary() returns (rep: StatusSummary)
    # Lists the caller org's recent traces — one row per trace with
    # its span count and wall-clock duration, most recently active first. This is
    # the trace SEARCH: it is where a trace id comes from, and the spans behind any
    # row are then read from GET /v1/o11y/traces/{traceId}. Every row belongs to the
    # caller's own org — the tenant is the validated principal, never an input, and
    # there is no administrator widening, because a trace list is a tenant's records
    # rather than a rollup over them. An unreachable telemetry store answers 503
    # rather than an empty page, because "no traces" and "cannot see the traces" are
    # different facts and only one of them is about the caller's system.
    get_o11y_traces(req: tracesIn) returns (rep: tracesOut)
    # Returns ingestion usage counts bucketed over the requested window,
    # optionally narrowed to one service.
    # Callers need the viewer role; the runtime's own gate enforces it.
    get_o11y_usage(req: O11yUsageIn)
    # Reports the running build: its version, whether an enterprise
    # edition is present ("N" in this build), and whether first-user setup has
    # completed.
    # Open by design; the runtime's own gate is OpenAccess.
    get_o11y_version() returns (rep: O11yVersionOut)
    # Changes a review queue's name, description or
    # score-config set. A field the request omits is left alone. A name another
    # queue in the same project already uses is a 409; a queue id belonging to
    # another org is a 404.
    patch_o11y_reviews_by_id(req: updateQueueIn) returns (rep: annQueueView)
    # Moves one queue item between PENDING and COMPLETED
    # and sets its assignee. Completing an item stamps its completedAt. An item that
    # exists under a different queue answers the same 404 an unknown item does, and
    # so does a queue belonging to another org.
    patch_o11y_reviews_by_id_items_by_itemid(req: updateItemIn) returns (rep: annItemView)
    # Counts the grouped exceptions in the query window for the caller's
    # org.
    # Callers need the viewer role; the runtime's own gate enforces it.
    post_o11y_counterrors(req: O11yErrorsCountIn)
    # Returns the service dependency graph over the requested
    # window: every parent→child edge observed, with call and error rates and
    # latency percentiles per edge.
    # Callers need the viewer role; the runtime's own gate enforces it.
    post_o11y_dependency_graph(req: O11yDependencyGraphIn)
    # Changes an issue's lifecycle — resolve, ignore, reopen or
    # assign — and returns the updated issue. Fields left unset are left unchanged.
    # Callers need the editor role; the runtime's own gate enforces it.
    post_o11y_errortracking_issues_by_id(req: O11yErrorUpdateIssueIn) returns (rep: O11yErrorIssueOut)
    # Lists the grouped exceptions in the query window — each an
    # exception type with its message, count, service and first/last-seen — for the
    # caller's org.
    # Callers need the viewer role; the runtime's own gate enforces it.
    post_o11y_listerrors(req: O11yErrorsListIn)
    # Changes how one log field is stored — selects or deselects it
    # as a materialized column and tunes its index — and echoes the setting back.
    # Callers need the editor role; the runtime's own gate enforces it.
    post_o11y_logs_fields(req: O11yFieldSetting) returns (rep: O11yFieldSetting)
    # Promotes and indexes log body paths: each named path is lifted
    # out of the JSON body into its own column, with the indexes the caller asked
    # for. Paths must start with "body.".
    # Callers need the editor role; the runtime's own gate enforces it.
    post_o11y_logs_promote_paths() returns (rep: O11yLogPromoteOut)
    # Analyzes a query and extracts the metric names it reads
    # and the columns it groups by.
    # Callers need the viewer role; the runtime's own gate enforces it.
    post_o11y_query_filter_analyze(req: O11yAnalyzeIn) returns (rep: O11yAnalyzeOut)
    # Creates the FIRST organization and its admin user. It is open by
    # design — there is nobody to be signed in as yet — and refuses once setup has
    # completed, after which new users arrive by invitation only.
    # Open by design; the runtime's own gate is OpenAccess.
    post_o11y_register(req: O11yRegisterIn) returns (rep: O11yRegisterOut)
    # Creates a human-review queue in the caller's org and
    # project. A name already used by another queue in the same project is a 409.
    post_o11y_reviews(req: createQueueReq) returns (rep: annQueueView)
    # Enqueues traces, observations or sessions on a review
    # queue. Each item names exactly one object, either by traceId / observationId /
    # sessionId or by objectType plus objectId; every item enters PENDING. A queue
    # id belonging to another org is a 404.
    post_o11y_reviews_by_id_items(req: addItemsIn) returns (rep: annItemsCreated)
    # Creates a Sentry project under the caller's org and
    # returns it, DSN included. Only the name, and optionally a slug and platform,
    # are the caller's to set; the org, id and key are server-assigned.
    # Callers need the editor role; the runtime's own gate enforces it.
    post_o11y_sentinel_projects(req: O11ySentryPostableProject) returns (rep: O11ySentryProjectOut)
    # Rotates a project's DSN key — bumping its rotation
    # watermark so keys below it stop verifying — and returns the project with its
    # new DSN.
    # Callers need the editor role; the runtime's own gate enforces it.
    post_o11y_sentinel_projects_by_id_keys_rotate(req: O11ySentryProjectRef) returns (rep: O11ySentryProjectOut)
    # Returns one service's entry-point operations with the
    # same latency and error profile topOperations reports.
    post_o11y_service_entry_point_operations(req: O11yOperationsIn) returns (rep: O11yOperationsOut)
    # Maps each service to its entry-point span names — for the
    # one service named in the request, or for every service when none is.
    post_o11y_service_top_level_operations(req: O11yTopLevelOpsIn)
    # Returns one service's heaviest operations in the window, each
    # with p50/p95/p99 latency, how often it ran and how often it errored.
    post_o11y_service_top_operations(req: O11yOperationsIn) returns (rep: O11yOperationsOut)
    # Lists the instrumented services seen in the window, each with the
    # request profile of its entry-point spans: p99 and average latency, call and
    # error rates, and the entry-point operations the numbers were computed over.
    post_o11y_services(req: O11yServicesIn) returns (rep: O11yServicesOut)
    # Sets one service's apdex threshold and the status codes excluded
    # from its score.
    # Admin only, as the mux tree has always gated it (AdminAccess); the runtime's
    # own gate enforces it.
    post_o11y_settings_apdex(req: O11yApdexSetIn) returns (rep: O11yApdexSetOut)
    # Sets the org's retention policy for one signal: the default TTL
    # in days, ordered per-label retention rules, and optional cold-storage
    # settings.
    # Admin only, as the mux tree has always gated it (AdminAccess); the runtime's
    # own gate enforces it.
    post_o11y_settings_ttl(req: O11yRetentionSetIn) returns (rep: O11yRetentionSetOut)
    # Changes an issue's lifecycle — resolve, ignore, reopen or
    # assign — and returns the updated issue. Fields left unset are left unchanged.
    # Callers need the editor role; the runtime's own gate enforces it.
    put_o11y_sentinel_issues_by_id(req: O11ySentryUpdateIssueIn) returns (rep: O11yErrorIssueOut)
}

# ---------------------------------------------------------------------
# 226 op(s) here. What follows is what this schema does not carry.
#
# dropped (7) — the value does not cross, and nothing fails:
#   O11yErrorWithSpan.Timestamp  time.Time  (empty message)
#   O11yNextPrevErrorIDs.NextTimestamp  time.Time  (empty message)
#   O11yNextPrevErrorIDs.PrevTimestamp  time.Time  (empty message)
#   O11yOrganization.CreatedAt  time.Time  (empty message)
#   O11yOrganization.UpdatedAt  time.Time  (empty message)
#   O11ySavedViewCreateOut.Data  valuer.UUID  (empty message)
#   PostableIngestionKey.ExpiresAt  time.Time  (empty message)
#
# blocked (199) — the op is absent; the field has no wire form:
#   AgentCheckIn  O11yAgentCheckInIn.PostableAgentCheckIn  cloudintegrationtypes.PostableAgentCheckIn  (reaches one)
#   AgentCheckIn  O11yAgentCheckInOut.Data  cloudintegrationtypes.GettableAgentCheckIn  (reaches one)
#   AgentCheckInDeprecated  O11yAgentCheckInIn  o11y.O11yAgentCheckInIn  (reaches one)
#   AgentCheckInDeprecated  O11yAgentCheckInOut  o11y.O11yAgentCheckInOut  (reaches one)
#   CreateAuthDomain  O11yPostableAuthDomain.Config  o11y.O11yAuthDomainConfig  (reaches one)
#   CreateChannel  PostableChannel.Receiver  alertmanagertypes.Receiver  (reaches one)
#   CreateRule  O11yRuleOut.Data  ruletypes.Rule  (reaches one)
#   CreateRule  PostableRule.Annotations  map[string]string  (map)
#   CreateRule  PostableRule.Evaluation  ruletypes.EvaluationEnvelope  (reaches one)
#   CreateRule  PostableRule.Labels  map[string]string  (map)
#   CreateRule  PostableRule.RuleCondition  ruletypes.RuleCondition  (reaches one)
#   CreateTraceFunnel  O11yFunnelOut.Data  tracefunneltypes.GettableFunnel  (reaches one)
#   GetAccount  O11yAccountOut.Data  cloudintegrationtypes.Account  (reaches one)
#   GetAccountService  O11yServiceOut.Data  cloudintegrationtypes.Service  (reaches one)
#   GetAlerts  O11yAlertsOut.Data  []*alertmanagertypes.DeprecatedGettableAlert  (no wire form)
#   GetAuthDomain  O11yAuthDomainOut.Data  o11y.O11yAuthDomain  (reaches one)
#   GetDraftFunnelErrorTraces  O11yDraftFunnelIn.Steps  []*tracefunneltypes.FunnelStep  (no wire form)
#   GetDraftFunnelErrorTraces  O11yFunnelRowsOut.Data  []o11y.O11yFunnelRow  (no wire form)
#   GetDraftFunnelOverview  O11yDraftFunnelIn  o11y.O11yDraftFunnelIn  (reaches one)
#   GetDraftFunnelOverview  O11yFunnelRowsOut  o11y.O11yFunnelRowsOut  (reaches one)
#   GetDraftFunnelSlowTraces  O11yDraftFunnelIn  o11y.O11yDraftFunnelIn  (reaches one)
#   GetDraftFunnelSlowTraces  O11yFunnelRowsOut  o11y.O11yFunnelRowsOut  (reaches one)
#   GetDraftFunnelStepMetrics  O11yDraftFunnelIn  o11y.O11yDraftFunnelIn  (reaches one)
#   GetDraftFunnelStepMetrics  O11yFunnelRowsOut  o11y.O11yFunnelRowsOut  (reaches one)
#   GetDraftFunnelStepOverview  O11yDraftFunnelIn  o11y.O11yDraftFunnelIn  (reaches one)
#   GetDraftFunnelStepOverview  O11yFunnelRowsOut  o11y.O11yFunnelRowsOut  (reaches one)
#   GetFlamegraph  O11yTraceFlamegraphIn.PostableFlamegraph  spantypes.PostableFlamegraph  (reaches one)
#   GetFlamegraph  O11yTraceFlamegraphOut.Data  spantypes.GettableFlamegraphTrace  (reaches one)
#   GetIntegration  O11yIntegrationOut.Data  integrations.Integration  (reaches one)
#   GetMetricReductionRuleTimeseries  O11yReductionSeriesOut.Data  o11y.O11yQueryRange  (reaches one)
#   GetOrgPreference  O11yPreferenceOut.Data  o11y.O11yPreference  (reaches one)
#   GetOverallStateTransitions  O11yRuleHistoryQueryIn.QueryRuleStateHistory  model.QueryRuleStateHistory  (reaches one)
#   GetRuleByID  O11yRuleOut  o11y.O11yRuleOut  (reaches one)
#   GetRuleHistoryFilterKeys  O11yRuleHistoryFilterKeysOut.Data  telemetrytypes.GettableFieldKeys  (reaches one)
#   GetRuleHistoryStats  O11yRuleHistoryStatsOut.Data  rulestatehistorytypes.GettableRuleStateHistoryStats  (reaches one)
#   GetRuleHistoryTimeline  O11yRuleHistoryTimelineOut.Data  rulestatehistorytypes.GettableRuleStateTimeline  (reaches one)
#   GetRuleHistoryTopContributors  O11yRuleHistoryContributorsOut.Data  []rulestatehistorytypes.GettableRuleStateHistoryContributor  (no wire form)
#   GetRuleStateHistory  O11yRuleHistoryQueryIn  o11y.O11yRuleHistoryQueryIn  (reaches one)
#   GetRuleStateHistory  O11yRuleStateTimelineOut.Data  model.RuleStateTimeline  (reaches one)
#   GetRuleStateHistoryTopContributors  O11yRuleHistoryQueryIn  o11y.O11yRuleHistoryQueryIn  (reaches one)
#   GetRuleStats  O11yRuleHistoryQueryIn  o11y.O11yRuleHistoryQueryIn  (reaches one)
#   GetRuleStats  O11yRuleStatsOut.Data  model.Stats  (reaches one)
#   GetService  O11yServiceOut  o11y.O11yServiceOut  (reaches one)
#   GetTraceAggregations  O11yTraceAggregationsIn.PostableTraceAggregations  spantypes.PostableTraceAggregations  (reaches one)
#   GetTraceAggregations  O11yTraceAggregationsOut.Data  spantypes.GettableTraceAggregations  (reaches one)
#   GetTraceFunnel  O11yFunnelOut  o11y.O11yFunnelOut  (reaches one)
#   GetTraceFunnelErrorTraces  O11yFunnelRowsOut  o11y.O11yFunnelRowsOut  (reaches one)
#   GetTraceFunnelOverview  O11yFunnelRowsOut  o11y.O11yFunnelRowsOut  (reaches one)
#   GetTraceFunnelSlowTraces  O11yFunnelRowsOut  o11y.O11yFunnelRowsOut  (reaches one)
#   GetTraceFunnelStepMetrics  O11yFunnelRowsOut  o11y.O11yFunnelRowsOut  (reaches one)
#   GetTraceFunnelStepOverview  O11yFunnelRowsOut  o11y.O11yFunnelRowsOut  (reaches one)
#   GetUserPreference  O11yPreferenceOut  o11y.O11yPreferenceOut  (reaches one)
#   GetWaterfallV4  O11yTraceWaterfallOut.Data  spantypes.GettableWaterfallTrace  (reaches one)
#   InspectMetrics  O11yMetricInspectOut.Data  o11y.O11yMetricSeriesSet  (reaches one)
#   InstallIntegration  InstallIntegrationRequest.Config  map[string]interface {}  (map)
#   ListAccounts  O11yAccountsOut.Data  cloudintegrationtypes.GettableAccounts  (reaches one)
#   ListAuthDomains  O11yAuthDomainsOut.Data  []o11y.O11yAuthDomain  (no wire form)
#   ListOrgPreferences  O11yPreferencesOut.Data  []o11y.O11yPreference  (no wire form)
#   ListRules  O11yRulesOut.Data  []*ruletypes.Rule  (no wire form)
#   ListTraceFunnels  O11yFunnelsOut.Data  []tracefunneltypes.GettableFunnel  (no wire form)
#   ListUserPreferences  O11yPreferencesOut  o11y.O11yPreferencesOut  (reaches one)
#   PatchRuleByID  O11yRuleOut  o11y.O11yRuleOut  (reaches one)
#   PatchRuleByID  O11yRuleUpdateIn.PostableRule  ruletypes.PostableRule  (reaches one)
#   TestChannel  Receiver.GoogleChatConfigs  []*alertmanagertypes.GoogleChatReceiverConfig  (no wire form)
#   TestChannel  Receiver.Receiver  config.Receiver  (reaches one)
#   TestChannelDeprecated  Receiver  alertmanagertypes.Receiver  (reaches one)
#   TestRule  PostableRule  ruletypes.PostableRule  (reaches one)
#   TestRuleNotification  PostableRule  ruletypes.PostableRule  (reaches one)
#   UpdateAuthDomain  O11yUpdatableAuthDomain.Config  o11y.O11yAuthDomainConfig  (reaches one)
#   UpdateChannelByID  O11yChannelUpdateIn.Receiver  alertmanagertypes.Receiver  (reaches one)
#   UpdateOrgPreference  O11yUpdatablePreference.Value  interface {}  (any)
#   UpdateRuleByID  O11yRuleUpdateIn  o11y.O11yRuleUpdateIn  (reaches one)
#   UpdateService  O11yUpdateServiceIn.UpdatableService  cloudintegrationtypes.UpdatableService  (reaches one)
#   UpdateTraceFunnel  O11yFunnelOut  o11y.O11yFunnelOut  (reaches one)
#   UpdateTraceFunnelSteps  O11yFunnelOut  o11y.O11yFunnelOut  (reaches one)
#   UpdateTraceFunnelSteps  O11yFunnelStepsUpdateIn.Steps  []*tracefunneltypes.FunnelStep  (no wire form)
#   UpdateUserPreference  O11yUpdatablePreference  o11y.O11yUpdatablePreference  (reaches one)
#   ValidateDraftFunnelTraces  O11yDraftFunnelIn  o11y.O11yDraftFunnelIn  (reaches one)
#   ValidateDraftFunnelTraces  O11yFunnelRowsOut  o11y.O11yFunnelRowsOut  (reaches one)
#   ValidateTraceFunnelTraces  O11yFunnelRowsOut  o11y.O11yFunnelRowsOut  (reaches one)
#   get_o11y_autocomplete_attribute_values  O11yAttributeValuesOut.Data  v3.FilterAttributeValueResponse  (reaches one)
#   get_o11y_clusters_attribute_values  O11yInfraAttributeValuesOut.Data  v3.FilterAttributeValueResponse  (reaches one)
#   get_o11y_daemonsets_attribute_values  O11yInfraAttributeValuesOut  o11y.O11yInfraAttributeValuesOut  (reaches one)
#   get_o11y_deployments_attribute_values  O11yInfraAttributeValuesOut  o11y.O11yInfraAttributeValuesOut  (reaches one)
#   get_o11y_errortracking_issues_by_id  O11yErrorGettableIssueOut.Data  o11y.O11yErrorGettableIssue  (reaches one)
#   get_o11y_explorer_views  O11ySavedViewListOut.Data  []v3.SavedView  (no wire form)
#   get_o11y_explorer_views_by_viewid  O11ySavedViewOut.Data  v3.SavedView  (reaches one)
#   get_o11y_features  O11yFeaturesOut.Data  []o11y.O11yFeature  (no wire form)
#   get_o11y_fields_keys  O11yFieldKeysOut.Data  telemetrytypes.GettableFieldKeys  (reaches one)
#   get_o11y_filter_suggestions  O11yFilterSuggestionsOut.Data  o11y.O11yFilterSuggestions  (reaches one)
#   get_o11y_hosts_attribute_values  O11yInfraAttributeValuesOut  o11y.O11yInfraAttributeValuesOut  (reaches one)
#   get_o11y_jobs_attribute_values  O11yInfraAttributeValuesOut  o11y.O11yInfraAttributeValuesOut  (reaches one)
#   get_o11y_licenses  O11yLicensesOut.Data  []interface {}  (no wire form)
#   get_o11y_licenses_active  O11yLicenseActiveOut.Data  interface {}  (any)
#   get_o11y_logs  O11yLogRecordsOut.Results  []o11y.O11yLogRow  (no wire form)
#   get_o11y_logs_aggregate  O11yLogAggregateOut.Items  map[int64]o11y.O11yLogAggregateBucket  (map)
#   get_o11y_logs_pipelines_by_version  O11yLogPipelinesOut.Data  o11y.O11yLogPipelines  (reaches one)
#   get_o11y_namespaces_attribute_values  O11yInfraAttributeValuesOut  o11y.O11yInfraAttributeValuesOut  (reaches one)
#   get_o11y_nodes_attribute_values  O11yInfraAttributeValuesOut  o11y.O11yInfraAttributeValuesOut  (reaches one)
#   get_o11y_pods_attribute_values  O11yInfraAttributeValuesOut  o11y.O11yInfraAttributeValuesOut  (reaches one)
#   get_o11y_processes_attribute_values  O11yInfraAttributeValuesOut  o11y.O11yInfraAttributeValuesOut  (reaches one)
#   get_o11y_pvcs_attribute_values  O11yInfraAttributeValuesOut  o11y.O11yInfraAttributeValuesOut  (reaches one)
#   get_o11y_sentinel_events_by_id  O11ySentryEventOut.Data  o11y.O11yEvent  (reaches one)
#   get_o11y_sentinel_issues_by_id  O11yErrorGettableIssueOut  o11y.O11yErrorGettableIssueOut  (reaches one)
#   get_o11y_sentinel_issues_by_id_events  O11ySentryIssueEventsOut.Data  o11y.O11yEvents  (reaches one)
#   get_o11y_sentinel_logs  O11yLogsOut.Data  o11y.O11yEvents  (reaches one)
#   get_o11y_sentinel_traces_by_id  O11yTraceOut.Data  o11y.O11yTraceDetail  (reaches one)
#   get_o11y_statefulsets_attribute_values  O11yInfraAttributeValuesOut  o11y.O11yInfraAttributeValuesOut  (reaches one)
#   get_o11y_stats  O11yOrgStatsOut.Data  map[string]interface {}  (map)
#   post_o11y_auto_complete_attribute_values  FilterAttributeValueRequest.ExistingFilterItems  []v3.FilterItem  (no wire form)
#   post_o11y_auto_complete_attribute_values  O11yAttributeValuesOut  o11y.O11yAttributeValuesOut  (reaches one)
#   post_o11y_clusters_list  ClusterListRequest.Filters  v3.FilterSet  (reaches one)
#   post_o11y_clusters_list  O11yClusterListOut.Data  model.ClusterListResponse  (reaches one)
#   post_o11y_daemonsets_list  DaemonSetListRequest.Filters  v3.FilterSet  (reaches one)
#   post_o11y_daemonsets_list  O11yDaemonSetListOut.Data  model.DaemonSetListResponse  (reaches one)
#   post_o11y_deployments_list  DeploymentListRequest.Filters  v3.FilterSet  (reaches one)
#   post_o11y_deployments_list  O11yDeploymentListOut.Data  model.DeploymentListResponse  (reaches one)
#   post_o11y_event  O11yEventIn.Attributes  map[string]interface {}  (map)
#   post_o11y_explorer_views  SavedView.CompositeQuery  v3.CompositeQuery  (reaches one)
#   post_o11y_hosts_list  HostListRequest.Filters  v3.FilterSet  (reaches one)
#   post_o11y_hosts_list  O11yHostListOut.Data  model.HostListResponse  (reaches one)
#   post_o11y_infra_monitoring_clusters  O11yInfraClustersOut.Data  inframonitoringtypes.Clusters  (reaches one)
#   post_o11y_infra_monitoring_clusters  PostableClusters.GroupBy  []querybuildertypesv5.GroupByKey  (no wire form)
#   post_o11y_infra_monitoring_clusters  PostableClusters.OrderBy  querybuildertypesv5.OrderBy  (reaches one)
#   post_o11y_infra_monitoring_daemonsets  O11yInfraDaemonSetsOut.Data  inframonitoringtypes.DaemonSets  (reaches one)
#   post_o11y_infra_monitoring_daemonsets  PostableDaemonSets.GroupBy  []querybuildertypesv5.GroupByKey  (no wire form)
#   post_o11y_infra_monitoring_daemonsets  PostableDaemonSets.OrderBy  querybuildertypesv5.OrderBy  (reaches one)
#   post_o11y_infra_monitoring_deployments  O11yInfraDeploymentsOut.Data  inframonitoringtypes.Deployments  (reaches one)
#   post_o11y_infra_monitoring_deployments  PostableDeployments.GroupBy  []querybuildertypesv5.GroupByKey  (no wire form)
#   post_o11y_infra_monitoring_deployments  PostableDeployments.OrderBy  querybuildertypesv5.OrderBy  (reaches one)
#   post_o11y_infra_monitoring_hosts  O11yInfraHostsOut.Data  inframonitoringtypes.Hosts  (reaches one)
#   post_o11y_infra_monitoring_hosts  PostableHosts.GroupBy  []querybuildertypesv5.GroupByKey  (no wire form)
#   post_o11y_infra_monitoring_hosts  PostableHosts.OrderBy  querybuildertypesv5.OrderBy  (reaches one)
#   post_o11y_infra_monitoring_jobs  O11yInfraJobsOut.Data  inframonitoringtypes.Jobs  (reaches one)
#   post_o11y_infra_monitoring_jobs  PostableJobs.GroupBy  []querybuildertypesv5.GroupByKey  (no wire form)
#   post_o11y_infra_monitoring_jobs  PostableJobs.OrderBy  querybuildertypesv5.OrderBy  (reaches one)
#   post_o11y_infra_monitoring_namespaces  O11yInfraNamespacesOut.Data  inframonitoringtypes.Namespaces  (reaches one)
#   post_o11y_infra_monitoring_namespaces  PostableNamespaces.GroupBy  []querybuildertypesv5.GroupByKey  (no wire form)
#   post_o11y_infra_monitoring_namespaces  PostableNamespaces.OrderBy  querybuildertypesv5.OrderBy  (reaches one)
#   post_o11y_infra_monitoring_nodes  O11yInfraNodesOut.Data  inframonitoringtypes.Nodes  (reaches one)
#   post_o11y_infra_monitoring_nodes  PostableNodes.GroupBy  []querybuildertypesv5.GroupByKey  (no wire form)
#   post_o11y_infra_monitoring_nodes  PostableNodes.OrderBy  querybuildertypesv5.OrderBy  (reaches one)
#   post_o11y_infra_monitoring_pods  O11yInfraPodsOut.Data  inframonitoringtypes.Pods  (reaches one)
#   post_o11y_infra_monitoring_pods  PostablePods.GroupBy  []querybuildertypesv5.GroupByKey  (no wire form)
#   post_o11y_infra_monitoring_pods  PostablePods.OrderBy  querybuildertypesv5.OrderBy  (reaches one)
#   post_o11y_infra_monitoring_pvcs  O11yInfraVolumesOut.Data  inframonitoringtypes.Volumes  (reaches one)
#   post_o11y_infra_monitoring_pvcs  PostableVolumes.GroupBy  []querybuildertypesv5.GroupByKey  (no wire form)
#   post_o11y_infra_monitoring_pvcs  PostableVolumes.OrderBy  querybuildertypesv5.OrderBy  (reaches one)
#   post_o11y_infra_monitoring_statefulsets  O11yInfraStatefulSetsOut.Data  inframonitoringtypes.StatefulSets  (reaches one)
#   post_o11y_infra_monitoring_statefulsets  PostableStatefulSets.GroupBy  []querybuildertypesv5.GroupByKey  (no wire form)
#   post_o11y_infra_monitoring_statefulsets  PostableStatefulSets.OrderBy  querybuildertypesv5.OrderBy  (reaches one)
#   post_o11y_jobs_list  JobListRequest.Filters  v3.FilterSet  (reaches one)
#   post_o11y_jobs_list  O11yJobListOut.Data  model.JobListResponse  (reaches one)
#   post_o11y_logs_pipelines  O11yLogPipelineCreateIn.Pipelines  []o11y.O11yLogPostablePipeline  (no wire form)
#   post_o11y_logs_pipelines  O11yLogPipelinesOut  o11y.O11yLogPipelinesOut  (reaches one)
#   post_o11y_logs_pipelines_preview  O11yLogPipelinePreviewIn.Logs  []o11y.O11yLogRecord  (no wire form)
#   post_o11y_logs_pipelines_preview  O11yLogPipelinePreviewIn.Pipelines  []o11y.O11yLogPipeline  (no wire form)
#   post_o11y_logs_pipelines_preview  O11yLogPipelinePreviewOut.Data  o11y.O11yLogPipelinePreview  (reaches one)
#   post_o11y_messaging-queues_kafka_consumer-lag_consumer-details  O11yQueueIn.Variables  map[string]string  (map)
#   post_o11y_messaging-queues_kafka_consumer-lag_network-latency  O11yQueueIn  o11y.O11yQueueIn  (reaches one)
#   post_o11y_messaging-queues_kafka_consumer-lag_producer-details  O11yQueueIn  o11y.O11yQueueIn  (reaches one)
#   post_o11y_messaging-queues_kafka_onboarding_consumers  O11yQueueIn  o11y.O11yQueueIn  (reaches one)
#   post_o11y_messaging-queues_kafka_onboarding_kafka  O11yQueueIn  o11y.O11yQueueIn  (reaches one)
#   post_o11y_messaging-queues_kafka_onboarding_producers  O11yQueueIn  o11y.O11yQueueIn  (reaches one)
#   post_o11y_messaging-queues_kafka_partition-latency_consumer  O11yQueueIn  o11y.O11yQueueIn  (reaches one)
#   post_o11y_messaging-queues_kafka_partition-latency_overview  O11yQueueIn  o11y.O11yQueueIn  (reaches one)
#   post_o11y_messaging-queues_kafka_span_evaluation  O11yQueueIn  o11y.O11yQueueIn  (reaches one)
#   post_o11y_messaging-queues_kafka_topic-throughput_consumer  O11yQueueIn  o11y.O11yQueueIn  (reaches one)
#   post_o11y_messaging-queues_kafka_topic-throughput_consumer-details  O11yQueueIn  o11y.O11yQueueIn  (reaches one)
#   post_o11y_messaging-queues_kafka_topic-throughput_producer  O11yQueueIn  o11y.O11yQueueIn  (reaches one)
#   post_o11y_messaging-queues_kafka_topic-throughput_producer-details  O11yQueueIn  o11y.O11yQueueIn  (reaches one)
#   post_o11y_messaging-queues_queue-overview  O11yQueueListIn.Filters  o11y.O11yQueueFilterSet  (reaches one)
#   post_o11y_messaging-queues_queue-overview  O11yQueueRowsOut.Data  []o11y.O11yQueueRow  (no wire form)
#   post_o11y_namespaces_list  NamespaceListRequest.Filters  v3.FilterSet  (reaches one)
#   post_o11y_namespaces_list  O11yNamespaceListOut.Data  model.NamespaceListResponse  (reaches one)
#   post_o11y_nodes_list  NodeListRequest.Filters  v3.FilterSet  (reaches one)
#   post_o11y_nodes_list  O11yNodeListOut.Data  model.NodeListResponse  (reaches one)
#   post_o11y_pods_list  O11yPodListOut.Data  model.PodListResponse  (reaches one)
#   post_o11y_pods_list  PodListRequest.Filters  v3.FilterSet  (reaches one)
#   post_o11y_processes_list  O11yProcessListOut.Data  model.ProcessListResponse  (reaches one)
#   post_o11y_processes_list  ProcessListRequest.Filters  v3.FilterSet  (reaches one)
#   post_o11y_pvcs_list  O11yPvcListOut.Data  model.VolumeListResponse  (reaches one)
#   post_o11y_pvcs_list  VolumeListRequest.Filters  v3.FilterSet  (reaches one)
#   post_o11y_query_range  QueryRangeRequest.CompositeQuery  querybuildertypesv5.CompositeQuery  (reaches one)
#   post_o11y_query_range  QueryRangeRequest.Variables  map[string]querybuildertypesv5.VariableItem  (map)
#   post_o11y_query_range_format  QueryRangeParamsV3.CompositeQuery  v3.CompositeQuery  (reaches one)
#   post_o11y_query_range_format  QueryRangeParamsV3.Variables  map[string]interface {}  (map)
#   post_o11y_query_range_preview  O11yQueryRangePreviewIn.QueryRangeRequest  querybuildertypesv5.QueryRangeRequest  (reaches one)
#   post_o11y_sentinel_discover  O11yDiscoverOut.Data  o11y.O11yTable  (reaches one)
#   post_o11y_span_percentile  O11ySpanPercentileIn.ResourceAttributes  map[string]string  (map)
#   post_o11y_statefulsets_list  O11yStatefulSetListOut.Data  model.StatefulSetListResponse  (reaches one)
#   post_o11y_statefulsets_list  StatefulSetListRequest.Filters  v3.FilterSet  (reaches one)
#   post_o11y_substitute_vars  QueryRangeRequest  querybuildertypesv5.QueryRangeRequest  (reaches one)
#   post_o11y_third-party-apis_overview_domain  O11yDomainsOut.Data  o11y.O11yDomainsAnswer  (reaches one)
#   post_o11y_third-party-apis_overview_list  O11yDomainsOut  o11y.O11yDomainsOut  (reaches one)
#   post_o11y_variables_query  O11yDashboardVarsIn.Variables  map[string]interface {}  (map)
#   post_o11y_variables_query  O11yDashboardVarsOut.Data  o11y.O11yDashboardVarValues  (reaches one)
#   put_o11y_explorer_views_by_viewid  O11ySavedViewOut  o11y.O11ySavedViewOut  (reaches one)
#   put_o11y_explorer_views_by_viewid  O11ySavedViewUpdateIn.SavedView  v3.SavedView  (reaches one)
#
# opaque (174) — crosses, arrives without its name:
#   O11yAPIKeyCreateOut.Data  o11y.O11yAPIKeySecret
#   O11yAPIKeysOut.Data  o11y.O11yAPIKey (list element)
#   O11yAggregateAttributesOut.Data  v3.AggregateAttributeResponse
#   O11yAnalyzeOut.Data  o11y.O11yQueryFilterAnalysis
#   O11yApdexOut.Data  o11y.O11yApdexSettings (list element)
#   O11yApdexSetOut.Data  o11y.O11yMessage
#   O11yAttributeKeysOut.Data  v3.FilterAttributeKeyResponse
#   O11yBulkInviteIn.Invites  o11y.O11yInviteIn (list element)
#   O11yChannelOut.Data  alertmanagertypes.Channel
#   O11yChannelsOut.Data  alertmanagertypes.Channel (list element)
#   O11yCheckOut.Data  o11y.O11yTransactionResult (list element)
#   O11yConnectionStatusOut.Data  integrations.IntegrationConnectionStatus
#   O11yCreateAccountIn.PostableAccount  cloudintegrationtypes.PostableAccount
#   O11yCreateAccountOut.Data  cloudintegrationtypes.GettableAccountWithConnectionArtifact
#   O11yCreateLimitIn.PostableIngestionKeyLimit  gatewaytypes.PostableIngestionKeyLimit
#   O11yCreatedIngestionKeyOut.Data  gatewaytypes.GettableCreatedIngestionKey
#   O11yCreatedLimitOut.Data  gatewaytypes.GettableCreatedIngestionKeyLimit
#   O11yCreatedOut.Data  o11y.O11yCreated
#   O11yCredentialsOut.Data  cloudintegrationtypes.Credentials
#   O11yDashboardListForUserOut.Data  o11y.O11yDashboardListForUser
#   O11yDashboardListOut.Data  o11y.O11yDashboardList
#   O11yDashboardOut.Data  o11y.O11yDashboard
#   O11yDashboardPatchIn.Ops  o11y.O11yDashboardPatchOp (list element)
#   O11yDashboardPostable.Tags  o11y.O11yDashboardPostableTag (list element)
#   O11yDashboardUpdateIn.O11yDashboardUpdatable  o11y.O11yDashboardUpdatable
#   O11yDashboardViewListOut.Data  o11y.O11yDashboardViewList
#   O11yDashboardViewOut.Data  o11y.O11yDashboardView
#   O11yDashboardViewPostable.Data  o11y.O11yDashboardViewData
#   O11yDashboardViewUpdateIn.O11yDashboardViewPostable  o11y.O11yDashboardViewPostable
#   O11yDependencyGraphIn.Tags  o11y.O11yTagFilter (list element)
#   O11yDeprecatedUserOut.Data  o11y.O11yDeprecatedUser
#   O11yDeprecatedUsersOut.Data  o11y.O11yDeprecatedUser (list element)
#   O11yDiscoverIn.Filters  o11y.O11yFilter (list element)
#   O11yDomainsIn.Filter  o11y.O11yDomainFilter
#   O11yDomainsIn.GroupBy  o11y.O11yDomainGroupBy (list element)
#   O11yDowntimeScheduleOut.Data  alertmanagertypes.PlannedMaintenance
#   O11yDowntimeSchedulesOut.Data  alertmanagertypes.PlannedMaintenance (list element)
#   O11yDowntimeUpdateIn.PostablePlannedMaintenance  alertmanagertypes.PostablePlannedMaintenance
#   O11yErrorIssueOut.Data  o11y.O11yErrorIssue
#   O11yErrorIssuesOut.Data  o11y.O11yErrorIssues
#   O11yErrorWithSpan.Timestamp  time.Time
#   O11yErrorsCountIn.Tags  o11y.O11yTagQuery (list element)
#   O11yErrorsListIn.Tags  o11y.O11yTagQuery (list element)
#   O11yFieldCatalogOut.Interesting  o11y.O11yTelemetryField (list element)
#   O11yFieldCatalogOut.Selected  o11y.O11yTelemetryField (list element)
#   O11yFieldValuesOut.Data  telemetrytypes.GettableFieldValues
#   O11yFunnelStepWindowIn.StepTransitionRequest  tracefunneltypes.StepTransitionRequest
#   O11yFunnelWindowIn.TimeRange  tracefunneltypes.TimeRange
#   O11yGettableHostOut.Data  zeustypes.GettableHost
#   O11yGlobalConfigOut.Data  o11y.O11yGlobalConfig
#   O11yIdentifiableOut.Data  o11y.O11yIdentifiable
#   O11yInfraAttributeKeysOut.Data  v3.FilterAttributeKeyResponse
#   O11yInfraChecksOut.Data  inframonitoringtypes.Checks
#   O11yIngestionKeysOut.Data  gatewaytypes.GettableIngestionKeys
#   O11yInstallOut.Data  integrations.IntegrationsListItem
#   O11yIntegrationsListOut.Data  integrations.IntegrationsListResponse
#   O11yInviteOut.Data  o11y.O11yInvite
#   O11yLLMAnnotationOut.Data  o11y.O11yLLMAnnotation
#   O11yLLMAnnotationsOut.Data  o11y.O11yLLMAnnotationsPage
#   O11yLLMObservationsOut.Data  o11y.O11yLLMObservationsPage
#   O11yLLMPricingRuleOut.Data  o11y.O11yLLMPricingRule
#   O11yLLMPricingRulesOut.Data  o11y.O11yLLMPricingRulesPage
#   O11yLLMScoreOut.Data  o11y.O11yLLMScore
#   O11yLLMScoresOut.Data  o11y.O11yLLMScoresPage
#   O11yLLMSessionsOut.Data  o11y.O11yLLMSessionsPage
#   O11yLLMTracesOut.Data  o11y.O11yLLMTracesPage
#   O11yLLMUpdatablePricingRules.Rules  o11y.O11yLLMUpdatablePricingRule (list element)
#   O11yLLMUsersOut.Data  o11y.O11yLLMUsersPage
#   O11yLogPromotedOut.Data  o11y.O11yLogPromotePath (list element)
#   O11yMetricAlertsOut.Data  o11y.O11yMetricAlerts
#   O11yMetricAttributesOut.Data  o11y.O11yMetricAttributes
#   O11yMetricDashboardsOut.Data  o11y.O11yMetricDashboards
#   O11yMetricHighlightsOut.Data  o11y.O11yMetricHighlights
#   O11yMetricInspectIn.Filter  o11y.O11yMetricFilter
#   O11yMetricListOut.Data  o11y.O11yMetricList
#   O11yMetricMetadataOut.Data  o11y.O11yMetricMetadata
#   O11yMetricOnboardingOut.Data  o11y.O11yMetricOnboarding
#   O11yMetricStatsIn.Filter  o11y.O11yMetricFilter
#   O11yMetricStatsIn.OrderBy  o11y.O11yMetricOrder
#   O11yMetricStatsOut.Data  o11y.O11yMetricStats
#   O11yMetricTreemapIn.Filter  o11y.O11yMetricFilter
#   O11yMetricTreemapOut.Data  o11y.O11yMetricTreemap
#   O11yNextPrevErrorIDs.NextTimestamp  time.Time
#   O11yNextPrevErrorIDs.PrevTimestamp  time.Time
#   O11yOnboardingOut.Data  o11y.O11yK8sOnboarding
#   O11yOperationsIn.Tags  o11y.O11yServiceTag (list element)
#   O11yOperationsOut.Data  o11y.O11yOperation (list element)
#   O11yOrganization.CreatedAt  time.Time
#   O11yOrganization.UpdatedAt  time.Time
#   O11yOrganizationOut.Data  o11y.O11yOrganization
#   O11yOverallStateTransitionsOut.Data  model.ReleStateItem (list element)
#   O11yPostableUser.UserRoles  o11y.O11yRoleID (list element)
#   O11yPromQueryOut.Data  o11y.O11yPromResult
#   O11yPublicDashboardDataOut.Data  o11y.O11yPublicDashboardData
#   O11yPublicDashboardOut.Data  o11y.O11yPublicDashboard
#   O11yPublicDashboardWriteIn.O11yPublicDashboardWrite  o11y.O11yPublicDashboardWrite
#   O11yQueueChecksOut.Data  o11y.O11yQueueCheck (list element)
#   O11yQuickFiltersOut.Data  o11y.O11ySignalFilters (list element)
#   O11yReductionRuleListOut.Data  o11y.O11yReductionRules
#   O11yReductionRuleOut.Data  o11y.O11yReductionRule
#   O11yReductionRulePreviewOut.Data  o11y.O11yReductionRulePreview
#   O11yReductionStatsOut.Data  o11y.O11yReductionStats
#   O11yRegisterOut.Data  o11y.O11yUser
#   O11yResetTokenOut.Data  o11y.O11yResetToken
#   O11yRetentionOut.TTLConditions  o11y.O11yRetentionRule (list element)
#   O11yRetentionSetIn.TTLConditions  o11y.O11yRetentionRule (list element)
#   O11yRoleCreateIn.TransactionGroups  o11y.O11yTransactionGroup (list element)
#   O11yRoleCreateOut.Data  o11y.O11yCreated
#   O11yRoleOut.Data  o11y.O11yRoleDetail
#   O11yRoleUpdateIn.TransactionGroups  o11y.O11yTransactionGroup (list element)
#   O11yRolesOut.Data  o11y.O11yRole (list element)
#   O11yRoutePoliciesOut.Data  alertmanagertypes.GettableRoutePolicy (list element)
#   O11yRoutePolicyOut.Data  alertmanagertypes.GettableRoutePolicy
#   O11yRoutePolicyUpdateIn.PostableRoutePolicy  alertmanagertypes.PostableRoutePolicy
#   O11yRuleHistoryFilterValuesIn.O11yRuleHistoryFilterKeysIn  o11y.O11yRuleHistoryFilterKeysIn
#   O11yRuleHistoryFilterValuesOut.Data  telemetrytypes.GettableFieldValues
#   O11yRuleHistoryOverallStatusOut.Data  rulestatehistorytypes.GettableRuleStateWindow (list element)
#   O11yRuleStateContributorsOut.Data  model.RuleStateHistoryContributor (list element)
#   O11ySavedViewCreateOut.Data  valuer.UUID
#   O11ySentryProjectOut.Data  o11y.O11ySentryProject
#   O11ySentryProjectsOut.Data  o11y.O11ySentryProjects
#   O11yServiceAccountCreateOut.Data  o11y.O11yCreated
#   O11yServiceAccountOut.Data  o11y.O11yServiceAccountDetail
#   O11yServiceAccountRolesOut.Data  o11y.O11yRole (list element)
#   O11yServiceAccountsOut.Data  o11y.O11yServiceAccount (list element)
#   O11yServicesIn.Tags  o11y.O11yServiceTag (list element)
#   O11yServicesMetadataOut.Data  cloudintegrationtypes.GettableServicesMetadata
#   O11yServicesOut.Data  o11y.O11yService (list element)
#   O11ySessionContextOut.Data  o11y.O11ySessionContext
#   O11ySignalFiltersOut.Data  o11y.O11ySignalFilters
#   O11ySpanMapperCreateIn.PostableSpanMapper  spantypes.PostableSpanMapper
#   O11ySpanMapperGroupOut.Data  spantypes.SpanMapperGroup
#   O11ySpanMapperGroupUpdateIn.UpdatableSpanMapperGroup  spantypes.UpdatableSpanMapperGroup
#   O11ySpanMapperGroupsOut.Data  spantypes.GettableSpanMapperGroups
#   O11ySpanMapperOut.Data  spantypes.SpanMapper
#   O11ySpanMapperUpdateIn.UpdatableSpanMapper  spantypes.UpdatableSpanMapper
#   O11ySpanMappersOut.Data  spantypes.GettableSpanMappers
#   O11ySpanPercentileOut.Data  o11y.O11ySpanPercentile
#   O11yStatsOut.Data  o11y.O11yStats
#   O11yTestNotificationOut.Data  o11y.O11yTestNotificationResult
#   O11yTestRuleOut.Data  ruletypes.GettableTestRule
#   O11yTokenOut.Data  o11y.O11yToken
#   O11yTraceWaterfallIn.PostableWaterfall  spantypes.PostableWaterfall
#   O11yTracesOut.Data  o11y.O11yTraces
#   O11yUpdatableQuickFilters.Filters  o11y.O11yFilterKey (list element)
#   O11yUpdateAccountIn.UpdatableAccount  cloudintegrationtypes.UpdatableAccount
#   O11yUpdateIngestionKeyIn.PostableIngestionKey  gatewaytypes.PostableIngestionKey
#   O11yUpdateLimitIn.UpdatableIngestionKeyLimit  gatewaytypes.UpdatableIngestionKeyLimit
#   O11yUserWithRolesOut.Data  o11y.O11yUserWithRoles
#   O11yUsersOut.Data  o11y.O11yUser (list element)
#   O11yWidgetQueryRangeOut.Data  o11y.O11yWidgetQueryRange
#   PostableIngestionKey.ExpiresAt  time.Time
#   PostablePlannedMaintenance.Schedule  alertmanagertypes.Schedule
#   PostableRoutePolicy.ExpressionKind  alertmanagertypes.ExpressionKind
#   PostableSpanMapperGroup.Condition  spantypes.SpanMapperGroupCondition
#   StatusSummary.InProgressMaintenances  o11y.StatusMaintenance (list element)
#   StatusSummary.OngoingIncidents  o11y.StatusIncident (list element)
#   StatusSummary.ScheduledMaintenances  o11y.StatusMaintenance (list element)
#   addItemsIn.Items  o11y.itemInput (list element)
#   annItemList.Data  o11y.annItemView (list element)
#   annItemList.Meta  o11y.listMeta
#   annItemsCreated.Data  o11y.annItemView (list element)
#   annQueueDetailView.Items  o11y.annItemView (list element)
#   annQueueList.Data  o11y.annQueueView (list element)
#   annQueueList.Meta  o11y.listMeta
#   availabilityResponse.Range  struct { SinceSec int "json:\"sinceSec\""; StepSec int "json:\"stepSec\"" }
#   availabilityResponse.Series  o11y.availabilityPoint (list element)
#   availabilityResponse.Services  o11y.serviceUp (list element)
#   metricsResponse.Range  struct { SinceSec int "json:\"sinceSec\""; StepSec int "json:\"stepSec\"" }
#   metricsResponse.Series  struct { Requests []o11y.point "json:\"requests\""; Errors []o11y.point "json:\"errors\""; LatencyP50Ms []o11y.point "json:\"latencyP50Ms\""; LatencyP95Ms []o11y.point "json:\"latencyP95Ms\"" }
#   metricsResponse.Summary  struct { Requests int64 "json:\"requests\""; Errors int64 "json:\"errors\""; ErrorRate float64 "json:\"errorRate\""; P95Ms float64 "json:\"p95Ms\"" }
#   metricsResponse.Usage  struct { Calls int64 "json:\"calls\""; Tokens int64 "json:\"tokens\""; CostCents int64 "json:\"costCents\""; Series []o11y.usageBucket "json:\"series\"" }
#   statusResult.Deployments  o11y.deployment (list element)
#   tracesOut.Traces  o11y.traceRow (list element)
