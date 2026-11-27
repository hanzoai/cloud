"use strict";
var __defProp = Object.defineProperty;
var __getOwnPropDesc = Object.getOwnPropertyDescriptor;
var __getOwnPropNames = Object.getOwnPropertyNames;
var __hasOwnProp = Object.prototype.hasOwnProperty;
var __export = (target, all) => {
  for (var name in all)
    __defProp(target, name, { get: all[name], enumerable: !0 });
}, __copyProps = (to, from, except, desc) => {
  if (from && typeof from == "object" || typeof from == "function")
    for (let key of __getOwnPropNames(from))
      !__hasOwnProp.call(to, key) && key !== except && __defProp(to, key, { get: () => from[key], enumerable: !(desc = __getOwnPropDesc(from, key)) || desc.enumerable });
  return to;
};
var __toCommonJS = (mod) => __copyProps(__defProp({}, "__esModule", { value: !0 }), mod);

// ../ap/packages/pieces/community/pandadoc/src/index.ts
var index_exports = {};
__export(index_exports, {
  pandadoc: () => pandadoc
});
module.exports = __toCommonJS(index_exports);
var import_pieces_framework16 = require("@activepieces/pieces-framework"), import_pieces_common14 = require("@activepieces/pieces-common");

// ../ap/packages/pieces/community/pandadoc/src/lib/actions/create-document-from-template.ts
var import_pieces_framework3 = require("@activepieces/pieces-framework"), import_pieces_common4 = require("@activepieces/pieces-common");

// ../ap/packages/pieces/community/pandadoc/src/lib/common/index.ts
var import_pieces_common2 = require("@activepieces/pieces-common");

// ../ap/packages/pieces/community/pandadoc/src/lib/common/auth.ts
var import_pieces_framework = require("@activepieces/pieces-framework"), import_pieces_common = require("@activepieces/pieces-common"), pandadocAuth = import_pieces_framework.PieceAuth.SecretText({
  displayName: "API Key",
  description: "Your PandaDoc API key. Get it from the Developer Dashboard in your PandaDoc account.",
  required: !0,
  validate: async ({ auth }) => {
    var _a, _b;
    try {
      return await import_pieces_common.httpClient.sendRequest({
        method: import_pieces_common.HttpMethod.GET,
        url: "https://api.pandadoc.com/public/v1/documents",
        headers: {
          Authorization: `API-Key ${auth}`
        }
      }), {
        valid: !0
      };
    } catch (error) {
      return {
        valid: !1,
        error: `Authentication failed: ${((_b = (_a = error == null ? void 0 : error.response) == null ? void 0 : _a.data) == null ? void 0 : _b.detail) || error.message}`
      };
    }
  }
});

// ../ap/packages/pieces/community/pandadoc/src/lib/common/index.ts
var BASE_URL = "https://api.pandadoc.com/public/v1", pandadocClient = {
  async makeRequest(auth, method, endpoint, body) {
    return (await import_pieces_common2.httpClient.sendRequest({
      method,
      url: `${BASE_URL}${endpoint}`,
      headers: {
        Authorization: `API-Key ${auth}`,
        "Content-Type": "application/json"
      },
      body: body ? JSON.stringify(body) : void 0
    })).body;
  },
  async makeFileRequest(auth, method, endpoint, file) {
    let formData = new FormData();
    return file && formData.append("file", file), (await import_pieces_common2.httpClient.sendRequest({
      method,
      url: `${BASE_URL}${endpoint}`,
      headers: {
        Authorization: `API-Key ${auth}`
      },
      body: formData
    })).body;
  }
};

// ../ap/packages/pieces/community/pandadoc/src/lib/common/dynamic-dropdowns.ts
var import_pieces_framework2 = require("@activepieces/pieces-framework"), import_pieces_common3 = require("@activepieces/pieces-common");
var documentDropdown = import_pieces_framework2.Property.Dropdown({
  auth: pandadocAuth,
  displayName: "Document",
  description: "Select a document from your PandaDoc workspace",
  required: !0,
  refreshers: [],
  options: async ({ auth }) => {
    if (!auth)
      return {
        disabled: !0,
        placeholder: "Please authenticate first",
        options: []
      };
    try {
      return {
        disabled: !1,
        options: (await pandadocClient.makeRequest(
          auth.secret_text,
          import_pieces_common3.HttpMethod.GET,
          "/documents?count=100&order_by=date_created"
        )).results.map((doc) => ({
          label: doc.name,
          value: doc.id
        }))
      };
    } catch (error) {
      return {
        disabled: !0,
        placeholder: "Failed to load documents",
        options: []
      };
    }
  }
}), templateDropdown = import_pieces_framework2.Property.Dropdown({
  auth: pandadocAuth,
  displayName: "Template",
  description: "Select a template from your PandaDoc workspace",
  required: !0,
  refreshers: [],
  options: async ({ auth }) => {
    if (!auth)
      return {
        disabled: !0,
        placeholder: "Please authenticate first",
        options: []
      };
    try {
      return {
        disabled: !1,
        options: (await pandadocClient.makeRequest(auth.secret_text, import_pieces_common3.HttpMethod.GET, "/templates?count=100")).results.map((template) => ({
          label: template.name,
          value: template.id
        }))
      };
    } catch (error) {
      return {
        disabled: !0,
        placeholder: "Failed to load templates",
        options: []
      };
    }
  }
}), templateFields = import_pieces_framework2.Property.DynamicProperties({
  displayName: "Template Fields",
  refreshers: ["template_uuid"],
  required: !1,
  auth: pandadocAuth,
  props: async ({ auth, template_uuid }) => {
    if (!auth || !template_uuid) return {};
    let fields = {}, response = await pandadocClient.makeRequest(
      auth,
      import_pieces_common3.HttpMethod.GET,
      `/templates/${template_uuid}/details`
    );
    for (let prop of response.fields)
      fields[prop.field_id] = import_pieces_framework2.Property.ShortText({
        displayName: prop.name,
        required: !1
      });
    return fields;
  }
}), folderDropdown = import_pieces_framework2.Property.Dropdown({
  auth: pandadocAuth,
  displayName: "Folder",
  description: "Select a folder from your PandaDoc workspace",
  required: !1,
  refreshers: [],
  options: async ({ auth }) => {
    if (!auth)
      return {
        disabled: !0,
        placeholder: "Please authenticate first",
        options: []
      };
    try {
      return {
        disabled: !1,
        options: (await pandadocClient.makeRequest(auth.secret_text, import_pieces_common3.HttpMethod.GET, "/documents/folders?count=100")).results.map((folder) => ({
          label: `${folder.name} - ${folder.uuid.substring(0, 8)}...`,
          value: folder.uuid
        }))
      };
    } catch (error) {
      return {
        disabled: !0,
        placeholder: "Failed to load folders",
        options: []
      };
    }
  }
}), contactDropdown = import_pieces_framework2.Property.Dropdown({
  auth: pandadocAuth,
  displayName: "Contact",
  description: "Select a contact from your PandaDoc workspace",
  required: !1,
  refreshers: [],
  options: async ({ auth }) => {
    if (!auth)
      return {
        disabled: !0,
        placeholder: "Please authenticate first",
        options: []
      };
    try {
      return {
        disabled: !1,
        options: (await pandadocClient.makeRequest(auth.secret_text, import_pieces_common3.HttpMethod.GET, "/contacts?count=100")).results.map((contact) => {
          let name = [contact.first_name, contact.last_name].filter(Boolean).join(" ") || "Unnamed", email = contact.email ? ` <${contact.email}>` : "";
          return {
            label: `${name}${email} - ${contact.id.substring(0, 8)}...`,
            value: contact.id
          };
        })
      };
    } catch (error) {
      return {
        disabled: !0,
        placeholder: "Failed to load contacts",
        options: []
      };
    }
  }
}), formDropdown = import_pieces_framework2.Property.Dropdown({
  auth: pandadocAuth,
  displayName: "Form",
  description: "Select a form from your PandaDoc workspace",
  required: !1,
  refreshers: [],
  options: async ({ auth }) => {
    if (!auth)
      return {
        disabled: !0,
        placeholder: "Please authenticate first",
        options: []
      };
    try {
      return {
        disabled: !1,
        options: (await pandadocClient.makeRequest(auth.secret_text, import_pieces_common3.HttpMethod.GET, "/forms?count=100")).results.map((form) => ({
          label: `${form.name} (${form.status}) - ${form.id.substring(0, 8)}...`,
          value: form.id
        }))
      };
    } catch (error) {
      return {
        disabled: !0,
        placeholder: "Failed to load forms",
        options: []
      };
    }
  }
}), memberDropdown = import_pieces_framework2.Property.Dropdown({
  auth: pandadocAuth,
  displayName: "Member",
  description: "Select a workspace member",
  required: !1,
  refreshers: [],
  options: async ({ auth }) => {
    if (!auth)
      return {
        disabled: !0,
        placeholder: "Please authenticate first",
        options: []
      };
    try {
      return {
        disabled: !1,
        options: (await pandadocClient.makeRequest(auth.secret_text, import_pieces_common3.HttpMethod.GET, "/members")).results.filter((member) => member.is_active).map((member) => ({
          label: `${[member.first_name, member.last_name].filter(Boolean).join(" ") || "Unnamed"} (${member.role}) - ${member.email}`,
          value: member.membership_id
        }))
      };
    } catch (error) {
      return {
        disabled: !0,
        placeholder: "Failed to load members",
        options: []
      };
    }
  }
}), documentAttachmentDropdown = import_pieces_framework2.Property.Dropdown({
  auth: pandadocAuth,
  displayName: "Attachment",
  description: "Select an attachment from the document",
  required: !0,
  refreshers: ["document_id"],
  options: async ({ auth, document_id }) => {
    if (!auth)
      return {
        disabled: !0,
        placeholder: "Please authenticate first",
        options: []
      };
    if (!document_id)
      return {
        disabled: !0,
        placeholder: "Please select a document first",
        options: []
      };
    try {
      return {
        disabled: !1,
        options: (await pandadocClient.makeRequest(
          auth.secret_text,
          import_pieces_common3.HttpMethod.GET,
          `/documents/${document_id}/attachments`
        )).map((attachment) => ({
          label: `${attachment.name || "Unnamed Attachment"}`,
          value: attachment.uuid
        }))
      };
    } catch (error) {
      return {
        disabled: !0,
        placeholder: "Failed to load attachments",
        options: []
      };
    }
  }
}), countryDropdown = import_pieces_framework2.Property.StaticDropdown({
  displayName: "Country",
  description: 'Select a country or choose "Other" to enter a custom country',
  required: !1,
  options: {
    options: [
      { label: "United States", value: "United States" },
      { label: "Canada", value: "Canada" },
      { label: "United Kingdom", value: "United Kingdom" },
      { label: "Australia", value: "Australia" },
      { label: "Germany", value: "Germany" },
      { label: "France", value: "France" },
      { label: "Spain", value: "Spain" },
      { label: "Italy", value: "Italy" },
      { label: "Netherlands", value: "Netherlands" },
      { label: "Sweden", value: "Sweden" },
      { label: "Norway", value: "Norway" },
      { label: "Denmark", value: "Denmark" },
      { label: "Finland", value: "Finland" },
      { label: "Brazil", value: "Brazil" },
      { label: "Mexico", value: "Mexico" },
      { label: "Japan", value: "Japan" },
      { label: "South Korea", value: "South Korea" },
      { label: "China", value: "China" },
      { label: "India", value: "India" },
      { label: "Singapore", value: "Singapore" },
      { label: "Switzerland", value: "Switzerland" },
      { label: "Austria", value: "Austria" },
      { label: "Belgium", value: "Belgium" },
      { label: "Poland", value: "Poland" },
      { label: "Ireland", value: "Ireland" },
      { label: "New Zealand", value: "New Zealand" },
      { label: "South Africa", value: "South Africa" },
      { label: "Argentina", value: "Argentina" },
      { label: "Chile", value: "Chile" },
      { label: "Other (Enter Custom)", value: "custom" }
    ]
  }
}), customCountryInput = import_pieces_framework2.Property.ShortText({
  displayName: "Custom Country",
  description: 'Enter your country name (only used if "Other" is selected above)',
  required: !1
}), stateDropdown = import_pieces_framework2.Property.StaticDropdown({
  displayName: "State/Province",
  description: 'Select a state/province or choose "Other" to enter a custom value',
  required: !1,
  options: {
    options: [
      // US States
      { label: "Alabama (US)", value: "Alabama" },
      { label: "Alaska (US)", value: "Alaska" },
      { label: "Arizona (US)", value: "Arizona" },
      { label: "Arkansas (US)", value: "Arkansas" },
      { label: "California (US)", value: "California" },
      { label: "Colorado (US)", value: "Colorado" },
      { label: "Connecticut (US)", value: "Connecticut" },
      { label: "Delaware (US)", value: "Delaware" },
      { label: "Florida (US)", value: "Florida" },
      { label: "Georgia (US)", value: "Georgia" },
      { label: "Hawaii (US)", value: "Hawaii" },
      { label: "Idaho (US)", value: "Idaho" },
      { label: "Illinois (US)", value: "Illinois" },
      { label: "Indiana (US)", value: "Indiana" },
      { label: "Iowa (US)", value: "Iowa" },
      { label: "Kansas (US)", value: "Kansas" },
      { label: "Kentucky (US)", value: "Kentucky" },
      { label: "Louisiana (US)", value: "Louisiana" },
      { label: "Maine (US)", value: "Maine" },
      { label: "Maryland (US)", value: "Maryland" },
      { label: "Massachusetts (US)", value: "Massachusetts" },
      { label: "Michigan (US)", value: "Michigan" },
      { label: "Minnesota (US)", value: "Minnesota" },
      { label: "Mississippi (US)", value: "Mississippi" },
      { label: "Missouri (US)", value: "Missouri" },
      { label: "Montana (US)", value: "Montana" },
      { label: "Nebraska (US)", value: "Nebraska" },
      { label: "Nevada (US)", value: "Nevada" },
      { label: "New Hampshire (US)", value: "New Hampshire" },
      { label: "New Jersey (US)", value: "New Jersey" },
      { label: "New Mexico (US)", value: "New Mexico" },
      { label: "New York (US)", value: "New York" },
      { label: "North Carolina (US)", value: "North Carolina" },
      { label: "North Dakota (US)", value: "North Dakota" },
      { label: "Ohio (US)", value: "Ohio" },
      { label: "Oklahoma (US)", value: "Oklahoma" },
      { label: "Oregon (US)", value: "Oregon" },
      { label: "Pennsylvania (US)", value: "Pennsylvania" },
      { label: "Rhode Island (US)", value: "Rhode Island" },
      { label: "South Carolina (US)", value: "South Carolina" },
      { label: "South Dakota (US)", value: "South Dakota" },
      { label: "Tennessee (US)", value: "Tennessee" },
      { label: "Texas (US)", value: "Texas" },
      { label: "Utah (US)", value: "Utah" },
      { label: "Vermont (US)", value: "Vermont" },
      { label: "Virginia (US)", value: "Virginia" },
      { label: "Washington (US)", value: "Washington" },
      { label: "West Virginia (US)", value: "West Virginia" },
      { label: "Wisconsin (US)", value: "Wisconsin" },
      { label: "Wyoming (US)", value: "Wyoming" },
      // Canadian Provinces
      { label: "Alberta (CA)", value: "Alberta" },
      { label: "British Columbia (CA)", value: "British Columbia" },
      { label: "Manitoba (CA)", value: "Manitoba" },
      { label: "New Brunswick (CA)", value: "New Brunswick" },
      {
        label: "Newfoundland and Labrador (CA)",
        value: "Newfoundland and Labrador"
      },
      { label: "Northwest Territories (CA)", value: "Northwest Territories" },
      { label: "Nova Scotia (CA)", value: "Nova Scotia" },
      { label: "Nunavut (CA)", value: "Nunavut" },
      { label: "Ontario (CA)", value: "Ontario" },
      { label: "Prince Edward Island (CA)", value: "Prince Edward Island" },
      { label: "Quebec (CA)", value: "Quebec" },
      { label: "Saskatchewan (CA)", value: "Saskatchewan" },
      { label: "Yukon (CA)", value: "Yukon" },
      // Other common ones
      { label: "England (UK)", value: "England" },
      { label: "Scotland (UK)", value: "Scotland" },
      { label: "Wales (UK)", value: "Wales" },
      { label: "Northern Ireland (UK)", value: "Northern Ireland" },
      { label: "New South Wales (AU)", value: "New South Wales" },
      { label: "Victoria (AU)", value: "Victoria" },
      { label: "Queensland (AU)", value: "Queensland" },
      { label: "Western Australia (AU)", value: "Western Australia" },
      { label: "South Australia (AU)", value: "South Australia" },
      { label: "Tasmania (AU)", value: "Tasmania" },
      { label: "Other (Enter Custom)", value: "custom" }
    ]
  }
}), customStateInput = import_pieces_framework2.Property.ShortText({
  displayName: "Custom State/Province",
  description: 'Enter your state/province name (only used if "Other" is selected above)',
  required: !1
}), jobTitleDropdown = import_pieces_framework2.Property.StaticDropdown({
  displayName: "Job Title",
  description: 'Select a common job title or choose "Other" to enter a custom title',
  required: !1,
  options: {
    options: [
      // Executive Level
      { label: "CEO / Chief Executive Officer", value: "CEO" },
      { label: "CTO / Chief Technology Officer", value: "CTO" },
      { label: "CFO / Chief Financial Officer", value: "CFO" },
      { label: "COO / Chief Operating Officer", value: "COO" },
      { label: "President", value: "President" },
      { label: "Vice President", value: "Vice President" },
      { label: "Director", value: "Director" },
      { label: "Manager", value: "Manager" },
      // Sales & Marketing
      { label: "Sales Manager", value: "Sales Manager" },
      { label: "Sales Representative", value: "Sales Representative" },
      { label: "Account Manager", value: "Account Manager" },
      {
        label: "Business Development Manager",
        value: "Business Development Manager"
      },
      { label: "Marketing Manager", value: "Marketing Manager" },
      { label: "Marketing Coordinator", value: "Marketing Coordinator" },
      // Technical
      { label: "Software Engineer", value: "Software Engineer" },
      { label: "Senior Software Engineer", value: "Senior Software Engineer" },
      { label: "Lead Developer", value: "Lead Developer" },
      { label: "DevOps Engineer", value: "DevOps Engineer" },
      { label: "Data Scientist", value: "Data Scientist" },
      { label: "Product Manager", value: "Product Manager" },
      { label: "IT Manager", value: "IT Manager" },
      // Operations
      { label: "Operations Manager", value: "Operations Manager" },
      { label: "Project Manager", value: "Project Manager" },
      { label: "Program Manager", value: "Program Manager" },
      { label: "Business Analyst", value: "Business Analyst" },
      { label: "Consultant", value: "Consultant" },
      // Finance & Legal
      { label: "Accountant", value: "Accountant" },
      { label: "Financial Analyst", value: "Financial Analyst" },
      { label: "Legal Counsel", value: "Legal Counsel" },
      { label: "Compliance Officer", value: "Compliance Officer" },
      // HR & Admin
      { label: "HR Manager", value: "HR Manager" },
      { label: "HR Coordinator", value: "HR Coordinator" },
      { label: "Office Manager", value: "Office Manager" },
      { label: "Administrative Assistant", value: "Administrative Assistant" },
      { label: "Executive Assistant", value: "Executive Assistant" },
      { label: "Other (Enter Custom)", value: "custom" }
    ]
  }
}), customJobTitleInput = import_pieces_framework2.Property.ShortText({
  displayName: "Custom Job Title",
  description: 'Enter a custom job title (only used if "Other" is selected above)',
  required: !1
}), industryDropdown = import_pieces_framework2.Property.StaticDropdown({
  displayName: "Industry",
  description: 'Select an industry or choose "Other" to enter a custom industry',
  required: !1,
  options: {
    options: [
      { label: "Technology", value: "Technology" },
      { label: "Software", value: "Software" },
      { label: "Financial Services", value: "Financial Services" },
      { label: "Healthcare", value: "Healthcare" },
      { label: "Manufacturing", value: "Manufacturing" },
      { label: "Retail", value: "Retail" },
      { label: "E-commerce", value: "E-commerce" },
      { label: "Education", value: "Education" },
      { label: "Consulting", value: "Consulting" },
      { label: "Real Estate", value: "Real Estate" },
      { label: "Construction", value: "Construction" },
      { label: "Legal Services", value: "Legal Services" },
      { label: "Marketing & Advertising", value: "Marketing & Advertising" },
      { label: "Media & Entertainment", value: "Media & Entertainment" },
      { label: "Telecommunications", value: "Telecommunications" },
      {
        label: "Transportation & Logistics",
        value: "Transportation & Logistics"
      },
      { label: "Energy & Utilities", value: "Energy & Utilities" },
      { label: "Automotive", value: "Automotive" },
      { label: "Aerospace & Defense", value: "Aerospace & Defense" },
      { label: "Pharmaceuticals", value: "Pharmaceuticals" },
      { label: "Biotechnology", value: "Biotechnology" },
      { label: "Insurance", value: "Insurance" },
      { label: "Banking", value: "Banking" },
      { label: "Investment", value: "Investment" },
      { label: "Non-Profit", value: "Non-Profit" },
      { label: "Government", value: "Government" },
      { label: "Agriculture", value: "Agriculture" },
      { label: "Food & Beverage", value: "Food & Beverage" },
      { label: "Hospitality & Tourism", value: "Hospitality & Tourism" },
      { label: "Sports & Recreation", value: "Sports & Recreation" },
      { label: "Other (Enter Custom)", value: "custom" }
    ]
  }
}), customIndustryInput = import_pieces_framework2.Property.ShortText({
  displayName: "Custom Industry",
  description: 'Enter a custom industry (only used if "Other" is selected above)',
  required: !1
}), recipientRoleDropdown = import_pieces_framework2.Property.StaticDropdown({
  displayName: "Recipient Role",
  description: 'Select a common recipient role or choose "Other" to enter a custom role',
  required: !1,
  options: {
    options: [
      { label: "Client", value: "Client" },
      { label: "Customer", value: "Customer" },
      { label: "Signer", value: "Signer" },
      { label: "Approver", value: "Approver" },
      { label: "Reviewer", value: "Reviewer" },
      { label: "Witness", value: "Witness" },
      { label: "Notary", value: "Notary" },
      { label: "Vendor", value: "Vendor" },
      { label: "Supplier", value: "Supplier" },
      { label: "Contractor", value: "Contractor" },
      { label: "Partner", value: "Partner" },
      { label: "Consultant", value: "Consultant" },
      { label: "Legal Counsel", value: "Legal Counsel" },
      { label: "Manager", value: "Manager" },
      { label: "Employee", value: "Employee" },
      { label: "HR Representative", value: "HR Representative" },
      { label: "Finance Team", value: "Finance Team" },
      { label: "Executive", value: "Executive" },
      { label: "Board Member", value: "Board Member" },
      { label: "Stakeholder", value: "Stakeholder" },
      { label: "Other (Enter Custom)", value: "custom" }
    ]
  }
}), customRecipientRoleInput = import_pieces_framework2.Property.ShortText({
  displayName: "Custom Recipient Role",
  description: 'Enter a custom recipient role (only used if "Other" is selected above)',
  required: !1
}), watermarkTextDropdown = import_pieces_framework2.Property.StaticDropdown({
  displayName: "Watermark Text",
  description: 'Select common watermark text or choose "Custom" to enter your own',
  required: !1,
  options: {
    options: [
      { label: "CONFIDENTIAL", value: "CONFIDENTIAL" },
      { label: "DRAFT", value: "DRAFT" },
      { label: "COPY", value: "COPY" },
      { label: "SAMPLE", value: "SAMPLE" },
      { label: "FOR REVIEW ONLY", value: "FOR REVIEW ONLY" },
      { label: "INTERNAL USE ONLY", value: "INTERNAL USE ONLY" },
      { label: "PROPRIETARY", value: "PROPRIETARY" },
      { label: "PRELIMINARY", value: "PRELIMINARY" },
      { label: "FINAL", value: "FINAL" },
      { label: "APPROVED", value: "APPROVED" },
      { label: "VOID", value: "VOID" },
      { label: "DUPLICATE", value: "DUPLICATE" },
      { label: "NOT FOR DISTRIBUTION", value: "NOT FOR DISTRIBUTION" },
      { label: "TRADE SECRET", value: "TRADE SECRET" },
      { label: "Custom (Enter Your Own)", value: "custom" }
    ]
  }
}), customWatermarkTextInput = import_pieces_framework2.Property.ShortText({
  displayName: "Custom Watermark Text",
  description: 'Enter custom watermark text (only used if "Custom" is selected above)',
  required: !1
}), tagDropdown = import_pieces_framework2.Property.StaticDropdown({
  displayName: "Tag",
  description: 'Select a common tag or choose "Custom" to enter your own',
  required: !1,
  options: {
    options: [
      { label: "contract", value: "contract" },
      { label: "proposal", value: "proposal" },
      { label: "quote", value: "quote" },
      { label: "invoice", value: "invoice" },
      { label: "agreement", value: "agreement" },
      { label: "nda", value: "nda" },
      { label: "employment", value: "employment" },
      { label: "vendor", value: "vendor" },
      { label: "client", value: "client" },
      { label: "urgent", value: "urgent" },
      { label: "priority", value: "priority" },
      { label: "review", value: "review" },
      { label: "approval", value: "approval" },
      { label: "legal", value: "legal" },
      { label: "finance", value: "finance" },
      { label: "hr", value: "hr" },
      { label: "sales", value: "sales" },
      { label: "marketing", value: "marketing" },
      { label: "template", value: "template" },
      { label: "final", value: "final" },
      { label: "draft", value: "draft" },
      { label: "Custom (Enter Your Own)", value: "custom" }
    ]
  }
}), customTagInput = import_pieces_framework2.Property.ShortText({
  displayName: "Custom Tag",
  description: 'Enter a custom tag (only used if "Custom" is selected above)',
  required: !1
});

// ../ap/packages/pieces/community/pandadoc/src/lib/actions/create-document-from-template.ts
var createDocumentFromTemplate = (0, import_pieces_framework3.createAction)({
  name: "createDocumentFromTemplate",
  displayName: "Create Document from Template",
  description: "Creates a document from a PandaDoc Template.",
  audience: "both",
  aiMetadata: {
    description: "Create a new PandaDoc document from an existing template, supplying recipients, prefilled field/token values, folder, tags, and metadata. Use to generate a contract or agreement that can then be sent for signature. Requires a valid template UUID; not idempotent \u2014 each call creates a separate document.",
    idempotent: !1
  },
  auth: pandadocAuth,
  props: {
    template_uuid: templateDropdown,
    name: import_pieces_framework3.Property.ShortText({
      displayName: "Document Name",
      description: "Name the document you are creating",
      required: !0
    }),
    recipients: import_pieces_framework3.Property.Array({
      displayName: "Recipients",
      description: "List of recipients to whom the document will be sent",
      required: !0,
      properties: {
        email: import_pieces_framework3.Property.ShortText({
          displayName: "Email",
          description: "Recipient email address",
          required: !1
        }),
        phone: import_pieces_framework3.Property.ShortText({
          displayName: "Phone",
          description: "Recipient phone number",
          required: !1
        }),
        first_name: import_pieces_framework3.Property.ShortText({
          displayName: "First Name",
          required: !1
        }),
        last_name: import_pieces_framework3.Property.ShortText({
          displayName: "Last Name",
          required: !1
        }),
        role: recipientRoleDropdown,
        custom_role: customRecipientRoleInput,
        signing_order: import_pieces_framework3.Property.Number({
          displayName: "Signing Order",
          description: "Set a signing order for recipient",
          required: !1
        })
      }
    }),
    fields: templateFields,
    tokens: import_pieces_framework3.Property.Array({
      displayName: "Variables/Tokens",
      description: "Pass values for the variables in the template",
      required: !1,
      properties: {
        name: import_pieces_framework3.Property.ShortText({
          displayName: "Variable Name",
          required: !0
        }),
        value: import_pieces_framework3.Property.ShortText({
          displayName: "Variable Value",
          required: !0
        })
      }
    }),
    folder_uuid: folderDropdown,
    tags: import_pieces_framework3.Property.Array({
      displayName: "Tags",
      description: "Tag the document for filtering and organization",
      required: !1,
      properties: {
        tag: tagDropdown,
        custom_tag: customTagInput
      }
    }),
    metadata: import_pieces_framework3.Property.Object({
      displayName: "Metadata",
      description: "Additional data in key-value format to associate with document",
      required: !1
    })
  },
  async run({ auth, propsValue }) {
    var _a;
    let body = {
      template_uuid: propsValue.template_uuid,
      name: propsValue.name,
      recipients: (_a = propsValue.recipients) == null ? void 0 : _a.map((recipient) => {
        let processedRecipient = {
          email: recipient.email,
          phone: recipient.phone,
          first_name: recipient.first_name,
          last_name: recipient.last_name,
          signing_order: recipient.signing_order
        };
        return recipient.role && (recipient.role === "custom" && recipient.custom_role ? processedRecipient.role = recipient.custom_role : recipient.role !== "custom" && (processedRecipient.role = recipient.role)), processedRecipient;
      })
    };
    return propsValue.fields && (body.fields = Object.fromEntries(
      Object.entries(propsValue.fields).filter(([_, value]) => value !== void 0 && value !== "").map(([key, value]) => [key, { value }])
    )), propsValue.tokens && (body.tokens = propsValue.tokens), propsValue.folder_uuid && (body.folder_uuid = propsValue.folder_uuid), propsValue.tags && (body.tags = propsValue.tags.map((tagItem) => tagItem.tag === "custom" && tagItem.custom_tag ? tagItem.custom_tag : tagItem.tag !== "custom" ? tagItem.tag : null).filter(Boolean)), propsValue.metadata && (body.metadata = propsValue.metadata), await pandadocClient.makeRequest(
      auth.secret_text,
      import_pieces_common4.HttpMethod.POST,
      "/documents",
      body
    );
  }
});

// ../ap/packages/pieces/community/pandadoc/src/lib/actions/create-attachment.ts
var import_pieces_framework4 = require("@activepieces/pieces-framework"), import_pieces_common5 = require("@activepieces/pieces-common");
var createAttachment = (0, import_pieces_framework4.createAction)({
  name: "createAttachment",
  displayName: "Create Attachment",
  description: "Adds an attachment to a document.",
  audience: "both",
  aiMetadata: {
    description: "Attach a file (fetched from a public file URL) to an existing PandaDoc document, with an optional display name. Requires the target document ID and a reachable source URL. Not idempotent \u2014 each call appends another attachment.",
    idempotent: !1
  },
  auth: pandadocAuth,
  props: {
    document_id: documentDropdown,
    source: import_pieces_framework4.Property.ShortText({
      displayName: "File URL",
      description: "URL link to the file to be attached to the document",
      required: !0
    }),
    name: import_pieces_framework4.Property.ShortText({
      displayName: "Attachment Name",
      description: "Optional name to set for the uploaded file.",
      required: !1
    })
  },
  async run({ auth, propsValue }) {
    let body = {
      source: propsValue.source
    };
    return propsValue.name && (body.name = propsValue.name), await pandadocClient.makeRequest(
      auth.secret_text,
      import_pieces_common5.HttpMethod.POST,
      `/documents/${propsValue.document_id}/attachments`,
      body
    );
  }
});

// ../ap/packages/pieces/community/pandadoc/src/lib/actions/create-or-update-contact.ts
var import_pieces_framework5 = require("@activepieces/pieces-framework"), import_pieces_common6 = require("@activepieces/pieces-common");
var createOrUpdateContact = (0, import_pieces_framework5.createAction)({
  name: "createOrUpdateContact",
  displayName: "Create or Update Contact",
  description: "Creates a new or update an existing contact.",
  audience: "both",
  aiMetadata: {
    description: "Create a new PandaDoc contact or update an existing one, depending on whether a contact ID is supplied: with an ID it patches that contact, without one it creates a new contact. Use to maintain recipient/contact records. Not idempotent in create mode \u2014 omitting the contact ID creates a duplicate on each call.",
    idempotent: !1
  },
  auth: pandadocAuth,
  props: {
    contact_id: import_pieces_framework5.Property.Dropdown({
      auth: pandadocAuth,
      displayName: "Contact ID (for Update)",
      description: "Select a contact to update. Leave empty to create a new contact.",
      required: !1,
      refreshers: [],
      options: async ({ auth }) => {
        if (!auth)
          return {
            disabled: !0,
            placeholder: "Please authenticate first",
            options: []
          };
        try {
          return {
            disabled: !1,
            options: (await pandadocClient.makeRequest(auth.secret_text, import_pieces_common6.HttpMethod.GET, "/contacts?count=100")).results.map((contact) => {
              let name = [contact.first_name, contact.last_name].filter(Boolean).join(" ") || "Unnamed", email = contact.email ? ` <${contact.email}>` : "";
              return {
                label: `${name}${email}`,
                value: contact.id
              };
            })
          };
        } catch (error) {
          return {
            disabled: !0,
            placeholder: "Failed to load contacts",
            options: []
          };
        }
      }
    }),
    email: import_pieces_framework5.Property.ShortText({
      displayName: "Email",
      description: "An email address of the contact",
      required: !1
    }),
    first_name: import_pieces_framework5.Property.ShortText({
      displayName: "First Name",
      description: "Contact's first name",
      required: !1
    }),
    last_name: import_pieces_framework5.Property.ShortText({
      displayName: "Last Name",
      description: "Contact's last name",
      required: !1
    }),
    company: import_pieces_framework5.Property.ShortText({
      displayName: "Company",
      description: "Contact's company name",
      required: !1
    }),
    job_title: jobTitleDropdown,
    custom_job_title: customJobTitleInput,
    industry: industryDropdown,
    custom_industry: customIndustryInput,
    phone: import_pieces_framework5.Property.ShortText({
      displayName: "Phone",
      description: "A phone number",
      required: !1
    }),
    country: countryDropdown,
    custom_country: customCountryInput,
    state: stateDropdown,
    custom_state: customStateInput,
    street_address: import_pieces_framework5.Property.ShortText({
      displayName: "Street Address",
      description: "A street address",
      required: !1
    }),
    city: import_pieces_framework5.Property.ShortText({
      displayName: "City",
      description: "A city name",
      required: !1
    }),
    postal_code: import_pieces_framework5.Property.ShortText({
      displayName: "Postal Code",
      description: "A postal code",
      required: !1
    })
  },
  async run({ auth, propsValue }) {
    let body = {};
    return propsValue.email !== void 0 && (body.email = propsValue.email), propsValue.first_name !== void 0 && (body.first_name = propsValue.first_name), propsValue.last_name !== void 0 && (body.last_name = propsValue.last_name), propsValue.company !== void 0 && (body.company = propsValue.company), propsValue.job_title !== void 0 && (propsValue.job_title === "custom" && propsValue.custom_job_title ? body.job_title = propsValue.custom_job_title : propsValue.job_title !== "custom" && (body.job_title = propsValue.job_title)), propsValue.industry !== void 0 && (propsValue.industry === "custom" && propsValue.custom_industry ? body.industry = propsValue.custom_industry : propsValue.industry !== "custom" && (body.industry = propsValue.industry)), propsValue.phone !== void 0 && (body.phone = propsValue.phone), propsValue.country !== void 0 && (propsValue.country === "custom" && propsValue.custom_country ? body.country = propsValue.custom_country : propsValue.country !== "custom" && (body.country = propsValue.country)), propsValue.state !== void 0 && (propsValue.state === "custom" && propsValue.custom_state ? body.state = propsValue.custom_state : propsValue.state !== "custom" && (body.state = propsValue.state)), propsValue.street_address !== void 0 && (body.street_address = propsValue.street_address), propsValue.city !== void 0 && (body.city = propsValue.city), propsValue.postal_code !== void 0 && (body.postal_code = propsValue.postal_code), propsValue.contact_id ? await pandadocClient.makeRequest(
      auth.secret_text,
      import_pieces_common6.HttpMethod.PATCH,
      `/contacts/${propsValue.contact_id}`,
      body
    ) : await pandadocClient.makeRequest(
      auth.secret_text,
      import_pieces_common6.HttpMethod.POST,
      "/contacts",
      body
    );
  }
});

// ../ap/packages/pieces/community/pandadoc/src/lib/actions/find-document.ts
var import_pieces_framework6 = require("@activepieces/pieces-framework"), import_pieces_common7 = require("@activepieces/pieces-common");
var findDocument = (0, import_pieces_framework6.createAction)({
  name: "findDocument",
  displayName: "Find Document(s)",
  description: "Finds documents based various filter.",
  audience: "both",
  aiMetadata: {
    description: "Search and list PandaDoc documents, optionally narrowed by query text, status, template, form, folder, contact, tags, date ranges, or metadata; with no filters it returns all documents (paginated). Use to locate documents or enumerate them before acting on a specific one. Read-only and idempotent.",
    idempotent: !0
  },
  auth: pandadocAuth,
  props: {
    q: import_pieces_framework6.Property.ShortText({
      displayName: "Search Query",
      description: "Search documents by name or reference number",
      required: !1
    }),
    id: import_pieces_framework6.Property.ShortText({
      displayName: "Document ID",
      description: "Search for document by its specific ID",
      required: !1
    }),
    status: import_pieces_framework6.Property.StaticDropdown({
      displayName: "Status",
      description: "Filter documents by status",
      required: !1,
      options: {
        options: [
          { label: "Draft", value: 0 },
          { label: "Sent", value: 1 },
          { label: "Completed", value: 2 },
          { label: "Uploaded", value: 3 },
          { label: "Error", value: 4 },
          { label: "Viewed", value: 5 },
          { label: "Waiting Approval", value: 6 },
          { label: "Approved", value: 7 },
          { label: "Rejected", value: 8 },
          { label: "Waiting Pay", value: 9 },
          { label: "Paid", value: 10 },
          { label: "Voided", value: 11 },
          { label: "Declined", value: 12 },
          { label: "External Review", value: 13 }
        ]
      }
    }),
    status__ne: import_pieces_framework6.Property.StaticDropdown({
      displayName: "Exclude Status",
      description: "Exclude documents with this status",
      required: !1,
      options: {
        options: [
          { label: "Draft", value: 0 },
          { label: "Sent", value: 1 },
          { label: "Completed", value: 2 },
          { label: "Uploaded", value: 3 },
          { label: "Error", value: 4 },
          { label: "Viewed", value: 5 },
          { label: "Waiting Approval", value: 6 },
          { label: "Approved", value: 7 },
          { label: "Rejected", value: 8 },
          { label: "Waiting Pay", value: 9 },
          { label: "Paid", value: 10 },
          { label: "Voided", value: 11 },
          { label: "Declined", value: 12 },
          { label: "External Review", value: 13 }
        ]
      }
    }),
    template_id: import_pieces_framework6.Property.Dropdown({
      auth: pandadocAuth,
      displayName: "Template",
      description: "Filter by parent template (cannot be used with Form)",
      required: !1,
      refreshers: [],
      options: async ({ auth }) => {
        if (!auth)
          return {
            disabled: !0,
            placeholder: "Please authenticate first",
            options: []
          };
        try {
          return {
            disabled: !1,
            options: (await pandadocClient.makeRequest(auth.secret_text, import_pieces_common7.HttpMethod.GET, "/templates?count=100")).results.map((template) => ({
              label: `${template.name} - ${template.id.substring(0, 8)}...`,
              value: template.id
            }))
          };
        } catch (error) {
          return {
            disabled: !0,
            placeholder: "Failed to load templates",
            options: []
          };
        }
      }
    }),
    form_id: formDropdown,
    folder_uuid: folderDropdown,
    contact_id: contactDropdown,
    membership_id: memberDropdown,
    tag: tagDropdown,
    custom_tag: customTagInput,
    created_from: import_pieces_framework6.Property.DateTime({
      displayName: "Created From",
      description: "Include only documents created on or after this date",
      required: !1
    }),
    created_to: import_pieces_framework6.Property.DateTime({
      displayName: "Created To",
      description: "Include only documents created before this date",
      required: !1
    }),
    completed_from: import_pieces_framework6.Property.DateTime({
      displayName: "Completed From",
      description: "Include only documents completed on or after this date",
      required: !1
    }),
    completed_to: import_pieces_framework6.Property.DateTime({
      displayName: "Completed To",
      description: "Include only documents completed before this date",
      required: !1
    }),
    modified_from: import_pieces_framework6.Property.DateTime({
      displayName: "Modified From",
      description: "Include only documents modified on or after this date",
      required: !1
    }),
    modified_to: import_pieces_framework6.Property.DateTime({
      displayName: "Modified To",
      description: "Include only documents modified before this date",
      required: !1
    }),
    deleted: import_pieces_framework6.Property.Checkbox({
      displayName: "Show Deleted Documents",
      description: "Return only deleted documents",
      required: !1
    }),
    count: import_pieces_framework6.Property.Number({
      displayName: "Results Count",
      description: "Number of documents to return (default: 50, max: 100)",
      required: !1
    }),
    page: import_pieces_framework6.Property.Number({
      displayName: "Page Number",
      description: "Page number for pagination (starts with 1)",
      required: !1
    }),
    order_by: import_pieces_framework6.Property.StaticDropdown({
      displayName: "Order By",
      description: "Sort results by field",
      required: !1,
      options: {
        options: [
          { label: "Date Status Changed (default)", value: "date_status_changed" },
          { label: "Date Created (ascending)", value: "date_created" },
          { label: "Date Created (descending)", value: "-date_created" },
          { label: "Date Modified (ascending)", value: "date_modified" },
          { label: "Date Modified (descending)", value: "-date_modified" },
          { label: "Name (ascending)", value: "name" },
          { label: "Name (descending)", value: "-name" }
        ]
      }
    }),
    metadata: import_pieces_framework6.Property.Object({
      displayName: "Metadata Filters",
      description: 'Filter by metadata key-value pairs. Use format: {"key": "value"}',
      required: !1
    })
  },
  async run({ auth, propsValue }) {
    let queryParams = {};
    if (propsValue.q && (queryParams.q = propsValue.q), propsValue.id && (queryParams.id = propsValue.id), propsValue.status !== void 0 && (queryParams.status = propsValue.status), propsValue.status__ne !== void 0 && (queryParams.status__ne = propsValue.status__ne), propsValue.template_id && (queryParams.template_id = propsValue.template_id), propsValue.form_id && (queryParams.form_id = propsValue.form_id), propsValue.folder_uuid && (queryParams.folder_uuid = propsValue.folder_uuid), propsValue.contact_id && (queryParams.contact_id = propsValue.contact_id), propsValue.membership_id && (queryParams.membership_id = propsValue.membership_id), propsValue.tag) {
      let tagValue;
      propsValue.tag === "custom" && propsValue.custom_tag ? tagValue = propsValue.custom_tag : propsValue.tag !== "custom" && (tagValue = propsValue.tag), tagValue && (queryParams.tag = tagValue);
    }
    if (propsValue.created_from && (queryParams.created_from = propsValue.created_from), propsValue.created_to && (queryParams.created_to = propsValue.created_to), propsValue.completed_from && (queryParams.completed_from = propsValue.completed_from), propsValue.completed_to && (queryParams.completed_to = propsValue.completed_to), propsValue.modified_from && (queryParams.modified_from = propsValue.modified_from), propsValue.modified_to && (queryParams.modified_to = propsValue.modified_to), propsValue.deleted && (queryParams.deleted = propsValue.deleted), propsValue.count && (queryParams.count = propsValue.count), propsValue.page && (queryParams.page = propsValue.page), propsValue.order_by && (queryParams.order_by = propsValue.order_by), propsValue.metadata)
      for (let [key, value] of Object.entries(propsValue.metadata))
        queryParams[`metadata_${key}`] = value;
    let queryString = new URLSearchParams(queryParams).toString(), endpoint = queryString ? `/documents?${queryString}` : "/documents";
    return await pandadocClient.makeRequest(
      auth.secret_text,
      import_pieces_common7.HttpMethod.GET,
      endpoint
    );
  }
});

// ../ap/packages/pieces/community/pandadoc/src/lib/actions/get-document-attachments.ts
var import_pieces_framework7 = require("@activepieces/pieces-framework"), import_pieces_common8 = require("@activepieces/pieces-common");
var getDocumentAttachments = (0, import_pieces_framework7.createAction)({
  name: "getDocumentAttachments",
  displayName: "Get Document Attachment",
  description: "Retrieves details of a specific attachment from a document.",
  audience: "both",
  aiMetadata: {
    description: "Retrieve details of a single attachment on a PandaDoc document, identified by document ID and attachment ID. Use to inspect a known attachment. Read-only and idempotent.",
    idempotent: !0
  },
  auth: pandadocAuth,
  props: {
    document_id: documentDropdown,
    attachment_id: documentAttachmentDropdown
  },
  async run({ auth, propsValue }) {
    return await pandadocClient.makeRequest(
      auth.secret_text,
      import_pieces_common8.HttpMethod.GET,
      `/documents/${propsValue.document_id}/attachments/${propsValue.attachment_id}`
    );
  }
});

// ../ap/packages/pieces/community/pandadoc/src/lib/actions/get-document-details.ts
var import_pieces_framework8 = require("@activepieces/pieces-framework"), import_pieces_common9 = require("@activepieces/pieces-common");
var getDocumentDetails = (0, import_pieces_framework8.createAction)({
  name: "getDocumentDetails",
  displayName: "Get Document",
  description: "Retrieves comprehensive document data.",
  audience: "both",
  aiMetadata: {
    description: "Retrieve the full detail record for a single PandaDoc document by its ID, including status, recipients, fields, and metadata. Use when you already know the document ID and need its complete data. Read-only and idempotent.",
    idempotent: !0
  },
  auth: pandadocAuth,
  props: {
    document_id: documentDropdown
  },
  async run({ auth, propsValue }) {
    return await pandadocClient.makeRequest(
      auth.secret_text,
      import_pieces_common9.HttpMethod.GET,
      `/documents/${propsValue.document_id}/details`
    );
  }
});

// ../ap/packages/pieces/community/pandadoc/src/lib/actions/download-document.ts
var import_pieces_framework9 = require("@activepieces/pieces-framework"), import_pieces_common10 = require("@activepieces/pieces-common");
var downloadDocument = (0, import_pieces_framework9.createAction)({
  name: "downloadDocument",
  displayName: "Download Document",
  description: "Downloads a document as PDF.",
  audience: "both",
  aiMetadata: {
    description: "Download a PandaDoc document by ID as a PDF file, optionally as a zip of separate per-section PDFs and with configurable watermark text, color, font size, and opacity. Use to fetch the rendered document for storage or forwarding. Read-only and idempotent.",
    idempotent: !0
  },
  auth: pandadocAuth,
  props: {
    document_id: documentDropdown,
    separate_files: import_pieces_framework9.Property.Checkbox({
      displayName: "Download as Separate Files",
      description: "Download document bundle as a zip archive of separate PDFs (1 file per section)",
      required: !1,
      defaultValue: !1
    }),
    watermark_text: watermarkTextDropdown,
    custom_watermark_text: customWatermarkTextInput,
    watermark_color: import_pieces_framework9.Property.StaticDropdown({
      displayName: "Watermark Color",
      description: "Select a watermark color or use custom HEX code",
      required: !1,
      options: {
        options: [
          { label: "Red (#FF0000)", value: "#FF0000" },
          { label: "Blue (#0000FF)", value: "#0000FF" },
          { label: "Green (#00FF00)", value: "#00FF00" },
          { label: "Black (#000000)", value: "#000000" },
          { label: "Gray (#808080)", value: "#808080" },
          { label: "Orange (#FF7F00)", value: "#FF7F00" },
          { label: "Purple (#800080)", value: "#800080" },
          { label: "Custom HEX Code", value: "custom" }
        ]
      }
    }),
    custom_watermark_color: import_pieces_framework9.Property.ShortText({
      displayName: "Custom Watermark Color",
      description: 'Enter a custom HEX color code (e.g., #FF5733). Only used if "Custom HEX Code" is selected above.',
      required: !1
    }),
    watermark_font_size: import_pieces_framework9.Property.Number({
      displayName: "Watermark Font Size",
      description: "Font size of the watermark text",
      required: !1
    }),
    watermark_opacity: import_pieces_framework9.Property.Number({
      displayName: "Watermark Opacity",
      description: "Opacity of the watermark (0.0 to 1.0)",
      required: !1
    })
  },
  async run({ auth, propsValue, files }) {
    let queryParams = {};
    if (propsValue.separate_files && (queryParams.separate_files = propsValue.separate_files), propsValue.watermark_text) {
      let watermarkText;
      propsValue.watermark_text === "custom" && propsValue.custom_watermark_text ? watermarkText = propsValue.custom_watermark_text : propsValue.watermark_text !== "custom" && (watermarkText = propsValue.watermark_text), watermarkText && (queryParams.watermark_text = watermarkText);
    }
    if (propsValue.watermark_color) {
      let colorValue = propsValue.watermark_color === "custom" ? propsValue.custom_watermark_color : propsValue.watermark_color;
      colorValue && (queryParams.watermark_color = colorValue);
    }
    propsValue.watermark_font_size && (queryParams.watermark_font_size = propsValue.watermark_font_size), propsValue.watermark_opacity !== void 0 && (queryParams.watermark_opacity = propsValue.watermark_opacity);
    let queryString = new URLSearchParams(queryParams).toString(), endpoint = `https://api.pandadoc.com/public/v1/documents/${propsValue.document_id}/download${queryString ? `?${queryString}` : ""}`, response = await import_pieces_common10.httpClient.sendRequest({
      method: import_pieces_common10.HttpMethod.GET,
      url: endpoint,
      headers: {
        Authorization: `API-Key ${auth.secret_text}`
      },
      responseType: "arraybuffer"
    });
    return {
      file: await files.write({
        fileName: "file.pdf",
        data: Buffer.from(response.body)
      })
    };
  }
});

// ../ap/packages/pieces/community/pandadoc/src/lib/triggers/document-completed.ts
var import_pieces_framework10 = require("@activepieces/pieces-framework"), import_pieces_common11 = require("@activepieces/pieces-common"), import_pieces_framework11 = require("@activepieces/pieces-framework");
var documentCompleted = (0, import_pieces_framework10.createTrigger)({
  name: "documentCompleted",
  displayName: "Document Completed",
  description: "Triggers when a document is completed.",
  aiMetadata: {
    description: "Fires when a PandaDoc document is completed \u2014 i.e. it reaches the completed state or all recipients finish signing. Optionally scoped to specific templates and/or folders. Represents a fully executed/signed document."
  },
  auth: pandadocAuth,
  props: {
    template_filter: import_pieces_framework10.Property.MultiSelectDropdown({
      auth: pandadocAuth,
      displayName: "Filter by Templates",
      description: "Only trigger for documents created from specific templates (leave empty for all)",
      required: !1,
      refreshers: [],
      options: async ({ auth }) => {
        if (!auth)
          return {
            disabled: !0,
            placeholder: "Please authenticate first",
            options: []
          };
        try {
          return {
            disabled: !1,
            options: (await pandadocClient.makeRequest(auth.secret_text, import_pieces_common11.HttpMethod.GET, "/templates?count=100")).results.map((template) => ({
              label: `${template.name} - ${template.id.substring(0, 8)}...`,
              value: template.id
            }))
          };
        } catch (error) {
          return {
            disabled: !0,
            placeholder: "Failed to load templates",
            options: []
          };
        }
      }
    }),
    folder_filter: import_pieces_framework10.Property.MultiSelectDropdown({
      auth: pandadocAuth,
      displayName: "Filter by Folders",
      description: "Only trigger for documents in specific folders (leave empty for all)",
      required: !1,
      refreshers: [],
      options: async ({ auth }) => {
        if (!auth)
          return {
            disabled: !0,
            placeholder: "Please authenticate first",
            options: []
          };
        try {
          return {
            disabled: !1,
            options: (await pandadocClient.makeRequest(auth.secret_text, import_pieces_common11.HttpMethod.GET, "/documents/folders?count=100")).results.map((folder) => ({
              label: `${folder.name} - ${folder.uuid.substring(0, 8)}...`,
              value: folder.uuid
            }))
          };
        } catch (error) {
          return {
            disabled: !0,
            placeholder: "Failed to load folders",
            options: []
          };
        }
      }
    })
  },
  sampleData: {
    event_type: "document_state_changed",
    data: {
      id: "sample_document_id",
      name: "Sample Contract",
      status: "document.completed",
      date_completed: "2024-01-15T10:30:00Z",
      template: {
        id: "template_id",
        name: "Contract Template"
      },
      folder_uuid: "folder_uuid",
      recipients: [
        {
          id: "recipient_id",
          email: "client@example.com",
          has_completed: !0,
          signature_date: "2024-01-15T10:30:00Z"
        }
      ]
    }
  },
  type: import_pieces_framework10.TriggerStrategy.WEBHOOK,
  async onEnable(context) {
    var _a;
    let webhookSubscription = await import_pieces_common11.httpClient.sendRequest({
      method: import_pieces_common11.HttpMethod.POST,
      url: "https://api.pandadoc.com/public/v1/webhook-subscriptions",
      headers: {
        Authorization: `API-Key ${context.auth.secret_text}`,
        "Content-Type": "application/json"
      },
      body: JSON.stringify({
        name: "Activepieces Document Completed",
        url: context.webhookUrl,
        active: !0,
        triggers: ["document_state_changed", "recipient_completed"],
        payload: ["fields", "products", "tokens", "metadata", "pricing"]
      })
    });
    await ((_a = context.store) == null ? void 0 : _a.put("webhook_id", webhookSubscription.body.uuid));
  },
  async onDisable(context) {
    var _a;
    let webhookId = await ((_a = context.store) == null ? void 0 : _a.get("webhook_id"));
    if (webhookId)
      try {
        await import_pieces_common11.httpClient.sendRequest({
          method: import_pieces_common11.HttpMethod.DELETE,
          url: `https://api.pandadoc.com/public/v1/webhook-subscriptions/${webhookId}`,
          headers: {
            Authorization: `API-Key ${context.auth.secret_text}`
          }
        });
      } catch (error) {
        console.log("Error cleaning up webhook:", error);
      }
  },
  async run(context) {
    var _a, _b, _c, _d, _e;
    let payload = context.payload.body;
    if (payload.event_type === "document_state_changed" && ((_a = payload.data) == null ? void 0 : _a.status) === "document.completed") {
      let documentData = payload.data;
      if (context.propsValue.template_filter && context.propsValue.template_filter.length > 0) {
        let templateId = (_b = documentData == null ? void 0 : documentData.template) == null ? void 0 : _b.id;
        if (!templateId || !context.propsValue.template_filter.includes(templateId))
          return [];
      }
      if (context.propsValue.folder_filter && context.propsValue.folder_filter.length > 0) {
        let folderUuid = documentData == null ? void 0 : documentData.folder_uuid;
        if (!folderUuid || !context.propsValue.folder_filter.includes(folderUuid))
          return [];
      }
      return [documentData];
    }
    if (payload.event_type === "recipient_completed" && ((_d = (_c = payload.data) == null ? void 0 : _c.recipients) == null ? void 0 : _d.every(
      (recipient) => recipient.has_completed
    ))) {
      let documentData = payload.data;
      if (context.propsValue.template_filter && context.propsValue.template_filter.length > 0) {
        let templateId = (_e = documentData == null ? void 0 : documentData.template) == null ? void 0 : _e.id;
        if (!templateId || !context.propsValue.template_filter.includes(templateId))
          return [];
      }
      if (context.propsValue.folder_filter && context.propsValue.folder_filter.length > 0) {
        let folderUuid = documentData == null ? void 0 : documentData.folder_uuid;
        if (!folderUuid || !context.propsValue.folder_filter.includes(folderUuid))
          return [];
      }
      return [documentData];
    }
    return [];
  },
  handshakeConfiguration: {
    strategy: import_pieces_framework11.WebhookHandshakeStrategy.NONE
  }
});

// ../ap/packages/pieces/community/pandadoc/src/lib/triggers/document-state-changed.ts
var import_pieces_framework12 = require("@activepieces/pieces-framework"), import_pieces_common12 = require("@activepieces/pieces-common"), import_pieces_framework13 = require("@activepieces/pieces-framework");
var documentStateChanged = (0, import_pieces_framework12.createTrigger)({
  name: "documentStateChanged",
  displayName: "Document State Changed",
  description: "Triggers when a document status changes.",
  aiMetadata: {
    description: "Fires whenever a PandaDoc document changes status (e.g. draft, sent, viewed, completed, paid, voided, declined), optionally filtered to specific target statuses. Represents a transition in a document lifecycle."
  },
  auth: pandadocAuth,
  props: {
    filter_status: import_pieces_framework12.Property.StaticMultiSelectDropdown({
      displayName: "Filter by Status",
      description: "Only trigger for specific status changes (leave empty for all)",
      required: !1,
      options: {
        options: [
          { label: "Draft", value: "document.draft" },
          { label: "Sent", value: "document.sent" },
          { label: "Completed", value: "document.completed" },
          { label: "Uploaded", value: "document.uploaded" },
          { label: "Error", value: "document.error" },
          { label: "Viewed", value: "document.viewed" },
          { label: "Waiting Approval", value: "document.waiting_approval" },
          { label: "Approved", value: "document.approved" },
          { label: "Rejected", value: "document.rejected" },
          { label: "Waiting Pay", value: "document.waiting_pay" },
          { label: "Paid", value: "document.paid" },
          { label: "Voided", value: "document.voided" },
          { label: "Declined", value: "document.declined" },
          { label: "External Review", value: "document.external_review" }
        ]
      }
    })
  },
  sampleData: {
    event_type: "document_state_changed",
    data: {
      id: "sample_document_id",
      name: "Sample Contract",
      status: "document.sent",
      previous_status: "document.draft",
      date_modified: "2024-01-15T10:30:00Z",
      date_sent: "2024-01-15T10:30:00Z",
      recipients: [
        {
          id: "recipient_id",
          email: "client@example.com",
          has_completed: !1,
          role: "Client"
        }
      ]
    }
  },
  type: import_pieces_framework12.TriggerStrategy.WEBHOOK,
  async onEnable(context) {
    var _a;
    let webhookSubscription = await import_pieces_common12.httpClient.sendRequest({
      method: import_pieces_common12.HttpMethod.POST,
      url: "https://api.pandadoc.com/public/v1/webhook-subscriptions",
      headers: {
        Authorization: `API-Key ${context.auth.secret_text}`,
        "Content-Type": "application/json"
      },
      body: JSON.stringify({
        name: "Activepieces Document State Changed",
        url: context.webhookUrl,
        active: !0,
        triggers: ["document_state_changed"],
        payload: ["fields", "products", "tokens", "metadata", "pricing"]
      })
    });
    await ((_a = context.store) == null ? void 0 : _a.put("webhook_id", webhookSubscription.body.uuid));
  },
  async onDisable(context) {
    var _a;
    let webhookId = await ((_a = context.store) == null ? void 0 : _a.get("webhook_id"));
    if (webhookId)
      try {
        await import_pieces_common12.httpClient.sendRequest({
          method: import_pieces_common12.HttpMethod.DELETE,
          url: `https://api.pandadoc.com/public/v1/webhook-subscriptions/${webhookId}`,
          headers: {
            Authorization: `API-Key ${context.auth.secret_text}`
          }
        });
      } catch (error) {
        console.log("Error cleaning up webhook:", error);
      }
  },
  async run(context) {
    let payload = context.payload.body;
    if (payload.event_type === "document_state_changed") {
      let documentData = payload.data, currentStatus = documentData == null ? void 0 : documentData.status;
      return context.propsValue.filter_status && context.propsValue.filter_status.length > 0 && !context.propsValue.filter_status.includes(currentStatus) ? [] : [documentData];
    }
    return [];
  },
  handshakeConfiguration: {
    strategy: import_pieces_framework13.WebhookHandshakeStrategy.NONE
  }
});

// ../ap/packages/pieces/community/pandadoc/src/lib/triggers/document-updated.ts
var import_pieces_framework14 = require("@activepieces/pieces-framework"), import_pieces_common13 = require("@activepieces/pieces-common"), import_pieces_framework15 = require("@activepieces/pieces-framework");
var documentUpdated = (0, import_pieces_framework14.createTrigger)({
  name: "documentUpdated",
  displayName: "Document Updated",
  description: "Triggers when a document is updated.",
  aiMetadata: {
    description: "Fires when a PandaDoc document is updated, optionally scoped to specific templates, folders, and/or update types (content, recipients, settings, status, fields, name, or metadata changes). Represents an edit to an existing document."
  },
  auth: pandadocAuth,
  props: {
    template_filter: import_pieces_framework14.Property.MultiSelectDropdown({
      auth: pandadocAuth,
      displayName: "Filter by Templates",
      description: "Only trigger for documents created from specific templates (leave empty for all)",
      required: !1,
      refreshers: [],
      options: async ({ auth }) => {
        if (!auth)
          return {
            disabled: !0,
            placeholder: "Please authenticate first",
            options: []
          };
        try {
          return {
            disabled: !1,
            options: (await pandadocClient.makeRequest(auth.secret_text, import_pieces_common13.HttpMethod.GET, "/templates?count=100")).results.map((template) => ({
              label: `${template.name} - ${template.id.substring(0, 8)}...`,
              value: template.id
            }))
          };
        } catch (error) {
          return {
            disabled: !0,
            placeholder: "Failed to load templates",
            options: []
          };
        }
      }
    }),
    folder_filter: import_pieces_framework14.Property.MultiSelectDropdown({
      auth: pandadocAuth,
      displayName: "Filter by Folders",
      description: "Only trigger for documents in specific folders (leave empty for all)",
      required: !1,
      refreshers: [],
      options: async ({ auth }) => {
        if (!auth)
          return {
            disabled: !0,
            placeholder: "Please authenticate first",
            options: []
          };
        try {
          return {
            disabled: !1,
            options: (await pandadocClient.makeRequest(auth.secret_text, import_pieces_common13.HttpMethod.GET, "/documents/folders?count=100")).results.map((folder) => ({
              label: `${folder.name} - ${folder.uuid.substring(0, 8)}...`,
              value: folder.uuid
            }))
          };
        } catch (error) {
          return {
            disabled: !0,
            placeholder: "Failed to load folders",
            options: []
          };
        }
      }
    }),
    update_type_filter: import_pieces_framework14.Property.StaticMultiSelectDropdown({
      displayName: "Filter by Update Type",
      description: "Only trigger for specific types of updates (leave empty for all)",
      required: !1,
      options: {
        options: [
          { label: "Content Changes", value: "content" },
          { label: "Recipient Changes", value: "recipients" },
          { label: "Settings Changes", value: "settings" },
          { label: "Status Changes", value: "status" },
          { label: "Field Changes", value: "fields" },
          { label: "Name Changes", value: "name" },
          { label: "Metadata Changes", value: "metadata" }
        ]
      }
    })
  },
  sampleData: {
    event_type: "document_updated",
    data: {
      id: "sample_document_id",
      name: "Sample Contract",
      status: "document.draft",
      date_modified: "2024-01-15T10:30:00Z",
      content_date_modified: "2024-01-15T10:30:00Z",
      updated_fields: ["name", "recipients"],
      template: {
        id: "template_id",
        name: "Contract Template"
      },
      folder_uuid: "folder_uuid",
      recipients: [
        {
          id: "recipient_id",
          email: "client@example.com",
          has_completed: !1,
          role: "Client"
        }
      ]
    }
  },
  type: import_pieces_framework14.TriggerStrategy.WEBHOOK,
  async onEnable(context) {
    var _a;
    let webhookSubscription = await import_pieces_common13.httpClient.sendRequest({
      method: import_pieces_common13.HttpMethod.POST,
      url: "https://api.pandadoc.com/public/v1/webhook-subscriptions",
      headers: {
        Authorization: `API-Key ${context.auth.secret_text}`,
        "Content-Type": "application/json"
      },
      body: JSON.stringify({
        name: "Activepieces Document Updated",
        url: context.webhookUrl,
        active: !0,
        triggers: ["document_updated"],
        payload: ["fields", "products", "tokens", "metadata", "pricing"]
      })
    });
    await ((_a = context.store) == null ? void 0 : _a.put("webhook_id", webhookSubscription.body.uuid));
  },
  async onDisable(context) {
    var _a;
    let webhookId = await ((_a = context.store) == null ? void 0 : _a.get("webhook_id"));
    if (webhookId)
      try {
        await import_pieces_common13.httpClient.sendRequest({
          method: import_pieces_common13.HttpMethod.DELETE,
          url: `https://api.pandadoc.com/public/v1/webhook-subscriptions/${webhookId}`,
          headers: {
            Authorization: `API-Key ${context.auth.secret_text}`
          }
        });
      } catch (error) {
        console.log("Error cleaning up webhook:", error);
      }
  },
  async run(context) {
    var _a;
    let payload = context.payload.body;
    if (payload.event_type === "document_updated") {
      let documentData = payload.data;
      if (context.propsValue.template_filter && context.propsValue.template_filter.length > 0) {
        let templateId = (_a = documentData == null ? void 0 : documentData.template) == null ? void 0 : _a.id;
        if (!templateId || !context.propsValue.template_filter.includes(templateId))
          return [];
      }
      if (context.propsValue.folder_filter && context.propsValue.folder_filter.length > 0) {
        let folderUuid = documentData == null ? void 0 : documentData.folder_uuid;
        if (!folderUuid || !context.propsValue.folder_filter.includes(folderUuid))
          return [];
      }
      if (context.propsValue.update_type_filter && context.propsValue.update_type_filter.length > 0) {
        let updatedFields = (documentData == null ? void 0 : documentData.updated_fields) || [];
        if (!context.propsValue.update_type_filter.some((filterType) => {
          switch (filterType) {
            case "content":
              return updatedFields.includes("content") || (documentData == null ? void 0 : documentData.content_date_modified);
            case "recipients":
              return updatedFields.includes("recipients");
            case "settings":
              return updatedFields.includes("settings");
            case "status":
              return updatedFields.includes("status");
            case "fields":
              return updatedFields.includes("fields");
            case "name":
              return updatedFields.includes("name");
            case "metadata":
              return updatedFields.includes("metadata");
            default:
              return !1;
          }
        }))
          return [];
      }
      return [documentData];
    }
    return [];
  },
  handshakeConfiguration: {
    strategy: import_pieces_framework15.WebhookHandshakeStrategy.NONE
  }
});

// ../ap/packages/pieces/community/pandadoc/src/index.ts
var import_pieces_framework17 = require("@activepieces/pieces-framework"), pandadoc = (0, import_pieces_framework16.createPiece)({
  displayName: "PandaDoc",
  auth: pandadocAuth,
  minimumSupportedRelease: "0.36.1",
  logoUrl: "https://cdn.activepieces.com/pieces/pandadoc.png",
  categories: [import_pieces_framework17.PieceCategory.CONTENT_AND_FILES, import_pieces_framework17.PieceCategory.PRODUCTIVITY],
  authors: ["onyedikachi-david"],
  actions: [
    createDocumentFromTemplate,
    createAttachment,
    createOrUpdateContact,
    findDocument,
    getDocumentAttachments,
    getDocumentDetails,
    downloadDocument,
    (0, import_pieces_common14.createCustomApiCallAction)({
      baseUrl: () => "https://api.pandadoc.com/public/v1",
      auth: pandadocAuth,
      authMapping: async (auth) => ({
        Authorization: `API-Key ${auth.secret_text}`
      })
    })
  ],
  triggers: [
    documentCompleted,
    documentStateChanged,
    documentUpdated
  ]
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  pandadoc
});
