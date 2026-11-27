# Generated from typed ops. Do not edit.
# Every struct and method below is derived from one op's In and Out, and
# every offset from the layout the op-call plane encodes against.

package environment

struct Environment {
    Repo      text       @0
    Install   text       @8
    Start     text       @16
    Secrets   list<text> @24
    State     text       @32
    Session   text       @40
    Proposal  bytes      @48
    UpdatedAt text       @56
}

struct envList {
    Data list<bytes> @0
}

struct envRef {
    Repo text @0
}

struct envSave {
    Repo    text @0
    Install text @8
    Start   text @16
}

struct envSecret {
    Repo  text @0
    Name  text @8
    Value text @16
}

struct envSecretRef {
    Repo text @0
    Name text @8
}

interface environment {
    # Forgets one codebase's environment, and its secrets with it.
    # Later runs on the codebase start bare. Each secret's value is removed from KMS
    # first; one that will not go stops the forget, so no value outlives the record
    # that names it. Org admin only.
    delete_environment_by_repo(req: envRef)
    # Removes one secret from a codebase.
    # The value is removed from KMS, then the name from the environment; later runs
    # no longer export it. Removing a name that is not set is a 404. Org admin only.
    delete_environment_by_repo_secrets_by_name(req: envSecretRef) returns (rep: Environment)
    # Lists every codebase in your org that has an environment.
    # An environment is what a coding run does to its checkout before its agent
    # starts: the install script, the start command and the secrets it exports.
    # Secret values are never answered — only which names are set. A codebase with
    # nothing saved and nothing proposed is not listed.
    get_environment() returns (rep: envList)
    # Reads one codebase's environment.
    # A codebase with none answers 200 with state `none` and empty scripts, so a
    # surface can always draw the editor. `proposed` means a setup run's answer is
    # waiting in `proposal`; nothing in it runs until it is saved.
    get_environment_by_repo(req: envRef) returns (rep: Environment)
    # Saves one codebase's install and start scripts.
    # Every later sandbox run on this codebase runs the install script in its fresh
    # checkout before the agent starts, and leaves the start command running in the
    # background. Saving clears any pending proposal: saving is the review. Secrets
    # are set one at a time on their own address and are left as they are. Org admin
    # only.
    put_environment_by_repo(req: envSave) returns (rep: Environment)
    # Sets one secret on a codebase.
    # The value is sealed in KMS and exported to every later sandbox run on this
    # codebase under its name, a setup run included. It is blinded out of every
    # stream the run publishes, and no route answers it back. Setting a name that is
    # already set replaces its value. Org admin only.
    put_environment_by_repo_secrets_by_name(req: envSecret) returns (rep: Environment)
}

# ---------------------------------------------------------------------
# 6 op(s) here. What follows is what this schema does not carry.
#
# opaque (2) — crosses, arrives without its name:
#   Environment.Proposal  environment.Proposal
#   envList.Data  environment.Environment (list element)
