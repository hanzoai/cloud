# Generated from typed ops. Do not edit.
# Every struct and method below is derived from one op's In and Out, and
# every offset from the layout the op-call plane encodes against.

package ml

struct mlRef {
    Name text @0
}

interface ml {
    # Deletes a deployed inference model. kserve owns the teardown: the
    # InferenceService goes away and the serving deployment behind it follows, so the
    # model stops answering predict calls. Answers 204, or 404 for a name the
    # caller's org does not own.
    delete_ml_models_by_name(req: mlRef)
}

# ---------------------------------------------------------------------
# 1 op(s) here. What follows is what this schema does not carry.
#
# blocked (5) — the op is absent; the field has no wire form:
#   get_ml_models  mlResourceList.Items  []ml.mlResource  (no wire form)
#   get_ml_models_by_name  mlResource.Spec  map[string]interface {}  (map)
#   get_ml_models_by_name  mlResource.Status  map[string]interface {}  (map)
#   post_ml_models  mlCreate.Labels  map[string]string  (map)
#   post_ml_models  mlResource  ml.mlResource  (reaches one)
