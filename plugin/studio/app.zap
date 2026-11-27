# Generated from typed ops. Do not edit.
# Every struct and method below is derived from one op's In and Out, and
# every offset from the layout the op-call plane encodes against.

package studio

struct Estimate {
    Cents i64         @0
    Steps list<bytes> @8
}

struct GpuList {
    Data list<bytes> @0
}

struct RunPatch {
    ID     text       @0
    Title  list<text> @8
    Shared list<bool> @16
}

struct Thread {
    ID       text        @0
    Title    text        @8
    Messages list<bytes> @16
    Org      text        @24
    Creator  text        @32
    Created  text        @40
    Updated  text        @48
}

struct ThreadPage {
    Data list<bytes> @0
    Next text        @8
}

struct documentsIn {
    Kind   text @0
    Limit  i64  @8
    Cursor text @16
}

struct pageIn {
    Limit  i64  @0
    Cursor text @8
}

struct ref {
    ID text @0
}

struct runsIn {
    Kind   text @0
    Status text @8
    Limit  i64  @16
    Cursor text @24
}

struct templatesIn {
    Kind   text @0
    Limit  i64  @8
    Cursor text @16
}

interface studio {
    # Deletes one of your brands.
    delete_studio_brands_by_id(req: ref)
    # Deletes one of your documents.
    delete_studio_documents_by_id(req: ref)
    # Deletes one of your flows.
    delete_studio_flows_by_id(req: ref)
    # Deletes one of your templates.
    delete_studio_templates_by_id(req: ref)
    # Deletes one of your threads. Its runs and their files stay.
    delete_studio_threads_by_id(req: ref)
    # Lists the GPUs your org has linked with `hanzo link` that can take a
    # studio.render step, each with whether it is free now. With none online, a
    # studio.render step waits in the queue.
    get_studio_gpus(req: pageIn) returns (rep: GpuList)
    # Lists your chat threads, most recent first.
    get_studio_threads(req: pageIn) returns (rep: ThreadPage)
    # Reads one of your threads and its messages, oldest first.
    get_studio_threads_by_id(req: ref) returns (rep: Thread)
}

# ---------------------------------------------------------------------
# 8 op(s) here. What follows is what this schema does not carry.
#
# blocked (41) — the op is absent; the field has no wire form:
#   get_studio_brands  BrandPage.Data  []studio.Brand  (no wire form)
#   get_studio_brands_by_id  Brand.Tokens  map[string]interface {}  (map)
#   get_studio_documents  DocumentPage.Data  []studio.Document  (no wire form)
#   get_studio_documents_by_id  Document.IR  studio.IR  (reaches one)
#   get_studio_flows  FlowPage.Data  []studio.Flow  (no wire form)
#   get_studio_flows_by_id  Flow.Graph  studio.Graph  (reaches one)
#   get_studio_flows_by_id  Flow.Inputs  map[string]interface {}  (map)
#   get_studio_runs  RunPage.Data  []studio.Run  (no wire form)
#   get_studio_runs_by_id  Run.Graph  studio.Graph  (reaches one)
#   get_studio_runs_by_id  Run.Input  map[string]interface {}  (map)
#   get_studio_runs_by_id_versions  RunPage  studio.RunPage  (reaches one)
#   get_studio_templates  TemplatePage.Data  []studio.Template  (no wire form)
#   get_studio_templates_by_id  Template.Graph  studio.Graph  (reaches one)
#   get_studio_templates_by_id  Template.Inputs  map[string]interface {}  (map)
#   patch_studio_brands_by_id  Brand  studio.Brand  (reaches one)
#   patch_studio_brands_by_id  BrandPatch.Tokens  map[string]interface {}  (map)
#   patch_studio_documents_by_id  Document  studio.Document  (reaches one)
#   patch_studio_documents_by_id  DocumentPatch.IR  studio.IR  (reaches one)
#   patch_studio_flows_by_id  Flow  studio.Flow  (reaches one)
#   patch_studio_flows_by_id  FlowPatch.Graph  studio.Graph  (reaches one)
#   patch_studio_flows_by_id  FlowPatch.Inputs  map[string]interface {}  (map)
#   patch_studio_runs_by_id  Run  studio.Run  (reaches one)
#   patch_studio_templates_by_id  Template  studio.Template  (reaches one)
#   patch_studio_templates_by_id  TemplatePatch.Graph  studio.Graph  (reaches one)
#   patch_studio_templates_by_id  TemplatePatch.Inputs  map[string]interface {}  (map)
#   post_studio_brands  Brand  studio.Brand  (reaches one)
#   post_studio_brands  BrandWrite.Tokens  map[string]interface {}  (map)
#   post_studio_documents  Document  studio.Document  (reaches one)
#   post_studio_documents  DocumentWrite.IR  studio.IR  (reaches one)
#   post_studio_estimate  RunCreate.Graph  studio.Graph  (reaches one)
#   post_studio_estimate  RunCreate.Input  map[string]interface {}  (map)
#   post_studio_flows  Flow  studio.Flow  (reaches one)
#   post_studio_flows  FlowWrite.Graph  studio.Graph  (reaches one)
#   post_studio_flows  FlowWrite.Inputs  map[string]interface {}  (map)
#   post_studio_runs  Run  studio.Run  (reaches one)
#   post_studio_runs  RunCreate  studio.RunCreate  (reaches one)
#   post_studio_runs_by_id_cancel  Run  studio.Run  (reaches one)
#   post_studio_runs_by_id_retry  Run  studio.Run  (reaches one)
#   post_studio_templates  Template  studio.Template  (reaches one)
#   post_studio_templates  TemplateWrite.Graph  studio.Graph  (reaches one)
#   post_studio_templates  TemplateWrite.Inputs  map[string]interface {}  (map)
#
# opaque (4) — crosses, arrives without its name:
#   Estimate.Steps  studio.StepPrice (list element)
#   GpuList.Data  studio.Gpu (list element)
#   Thread.Messages  studio.Message (list element)
#   ThreadPage.Data  studio.ThreadCard (list element)
