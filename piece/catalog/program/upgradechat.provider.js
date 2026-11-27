"use strict";
var __defProp = Object.defineProperty;
var __getOwnPropDesc = Object.getOwnPropertyDescriptor;
var __getOwnPropNames = Object.getOwnPropertyNames, __getOwnPropSymbols = Object.getOwnPropertySymbols;
var __hasOwnProp = Object.prototype.hasOwnProperty, __propIsEnum = Object.prototype.propertyIsEnumerable;
var __defNormalProp = (obj, key, value) => key in obj ? __defProp(obj, key, { enumerable: !0, configurable: !0, writable: !0, value }) : obj[key] = value, __spreadValues = (a, b) => {
  for (var prop in b || (b = {}))
    __hasOwnProp.call(b, prop) && __defNormalProp(a, prop, b[prop]);
  if (__getOwnPropSymbols)
    for (var prop of __getOwnPropSymbols(b))
      __propIsEnum.call(b, prop) && __defNormalProp(a, prop, b[prop]);
  return a;
};
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

// ../ap/packages/pieces/community/upgradechat/src/index.ts
var index_exports = {};
__export(index_exports, {
  upgradechat: () => upgradechat
});
module.exports = __toCommonJS(index_exports);
var import_pieces_framework11 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/upgradechat/src/lib/actions/add-or-update-contact-extended.ts
var import_pieces_framework2 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/upgradechat/src/lib/auth.ts
var import_pieces_framework = require("@activepieces/pieces-framework"), markdownDescription = `
  Follow these instructions to get your Upgrade.chat API Key:

  1. Visit the following website: https://crm.upgrade.chat/ or the beta website: https://betacrm.upgrade.chat/
  2. Once on the website, locate and click on the admin to obtain your Upgrade.chat API Key.
`, upgradechatAuth = import_pieces_framework.PieceAuth.CustomAuth({
  description: markdownDescription,
  required: !0,
  props: {
    base_url: import_pieces_framework.Property.StaticDropdown({
      displayName: "Base URL",
      description: "Select the base environment URL",
      required: !0,
      options: {
        disabled: !1,
        options: [
          {
            label: "Upgrade.chat Live (crm.upgrade.chat)",
            value: "https://crm.upgrade.chat/"
          },
          {
            label: "Upgrade.chat Beta (betacrm.upgrade.chat)",
            value: "https://betacrm.upgrade.chat/"
          }
        ]
      }
    }),
    api_key: import_pieces_framework.PieceAuth.SecretText({
      displayName: "Secret API Key",
      description: "Enter the API Key",
      required: !0
    })
  }
});

// ../ap/packages/pieces/community/upgradechat/src/lib/actions/add-or-update-contact-extended.ts
var import_pieces_common = require("@activepieces/pieces-common");
var addOrUpdateContactExtended = (0, import_pieces_framework2.createAction)({
  name: "addOrUpdateContactExtended",
  displayName: "Add or Update Contact (Extended)",
  description: "Adds or updates a contact (extended version)",
  audience: "both",
  aiMetadata: {
    description: "Imports a contact into the Sperse/Upgrade.chat CRM with the full field set: personal, business, home/work/company addresses, links, custom fields, optional user creation, tracking/UTM data, and up to three inline subscriptions. When 'Match Existing Contact' is enabled (default) or a Contact ID / Contact XREF is supplied it updates the existing record (upsert), otherwise it inserts a new one. Prefer this over the basic Add or Update Contact when you need company, address, or subscription detail. First or Last name is required (or a Company Name). Idempotent only when matching is enabled or a stable Contact ID/XREF is reused.",
    idempotent: !0
  },
  auth: upgradechatAuth,
  props: {
    matchExisting: import_pieces_framework2.Property.StaticDropdown({
      displayName: "Match Existing Contact",
      description: 'If "Yes", will try to find an existing record using Email and Full Name and update it.',
      required: !1,
      defaultValue: !0,
      options: {
        disabled: !1,
        options: [
          {
            label: "Yes",
            value: !0
          },
          {
            label: "No",
            value: !1
          }
        ]
      }
    }),
    overrideLists: import_pieces_framework2.Property.StaticDropdown({
      displayName: "Override Lists",
      description: 'If "Yes", will override lists of contact details in update mode instead of merging them - lists, tags, emails, phones, links, addresses, photos.',
      required: !1,
      defaultValue: !1,
      options: {
        disabled: !1,
        options: [
          {
            label: "Yes",
            value: !0
          },
          {
            label: "No",
            value: !1
          }
        ]
      }
    }),
    ignoreInvalidValues: import_pieces_framework2.Property.StaticDropdown({
      displayName: "Ignore Invalid Values",
      description: 'If "Yes", will save the record even if there are some validation errors.',
      required: !1,
      defaultValue: !0,
      options: {
        disabled: !1,
        options: [
          {
            label: "Yes",
            value: !0
          },
          {
            label: "No",
            value: !1
          }
        ]
      }
    }),
    contactId: import_pieces_framework2.Property.Number({
      displayName: "Contact ID",
      description: "Sperse Contact ID. Will be used for looking a client",
      defaultValue: 0,
      required: !1
    }),
    contactXref: import_pieces_framework2.Property.ShortText({
      displayName: "Contact XREF",
      description: "This is string external reference that can be passed during creation and then if sent again it will update the record.",
      required: !1
    }),
    importType: import_pieces_framework2.Property.StaticDropdown({
      displayName: "Contact Type",
      required: !0,
      defaultValue: "Lead",
      options: {
        disabled: !1,
        options: [
          {
            label: "Lead",
            value: "Lead"
          },
          {
            label: "Client",
            value: "Client"
          },
          {
            label: "Partner",
            value: "Partner"
          }
        ]
      }
    }),
    // user info
    createUser: import_pieces_framework2.Property.StaticDropdown({
      displayName: "Create User",
      description: 'If "Yes" then User will be created. Personal email will be used as User Name.',
      required: !1,
      defaultValue: !0,
      options: {
        disabled: !1,
        options: [
          {
            label: "Yes",
            value: !0
          },
          {
            label: "No",
            value: !1
          }
        ]
      }
    }),
    sendWelcomeEmail: import_pieces_framework2.Property.StaticDropdown({
      displayName: "Send Welcome Email",
      description: 'If "Yes" then Welcome Email will be sent to the newly created user.',
      required: !1,
      defaultValue: !0,
      options: {
        disabled: !1,
        options: [
          {
            label: "Yes",
            value: !0
          },
          {
            label: "No",
            value: !1
          }
        ]
      }
    }),
    userPassword: import_pieces_framework2.Property.ShortText({
      displayName: "User Password",
      description: "If password is not passed then it will be automatically generated.",
      required: !1
    }),
    // fullname
    namePrefix: import_pieces_framework2.Property.ShortText({
      displayName: "Prefix",
      description: "The title used to address the contact.",
      required: !1
    }),
    firstName: import_pieces_framework2.Property.ShortText({
      displayName: "First Name",
      description: "Required if Last Name and Company Name fields are empty.",
      required: !0
    }),
    middleName: import_pieces_framework2.Property.ShortText({
      displayName: "Middle Name",
      description: "The contact's middle name.",
      required: !1
    }),
    lastName: import_pieces_framework2.Property.ShortText({
      displayName: "Last Name",
      description: "Required if First Name and Company Name fields are empty.",
      required: !0
    }),
    nickName: import_pieces_framework2.Property.ShortText({
      displayName: "Nick Name",
      description: "The contact's nick name.",
      required: !1
    }),
    nameSuffix: import_pieces_framework2.Property.ShortText({
      displayName: "Suffix",
      description: "Additional information about the contact e.g PhD.",
      required: !1
    }),
    // personal info
    gender: import_pieces_framework2.Property.StaticDropdown({
      displayName: "Gender",
      required: !1,
      options: {
        disabled: !1,
        options: [
          {
            label: "Male",
            value: "Male"
          },
          {
            label: "Female",
            value: "Female"
          }
        ]
      }
    }),
    dob: import_pieces_framework2.Property.ShortText({
      displayName: "Date of Birth",
      description: "Valid date format YYYY-MM-DD or MM-DD-YYYY.",
      required: !1
    }),
    bankCode: import_pieces_framework2.Property.ShortText({
      displayName: "Bank Code",
      description: "The contact's 4-letter personality code.",
      required: !1
    }),
    ssn: import_pieces_framework2.Property.ShortText({
      displayName: "SSN",
      description: "The contact's social security number.",
      required: !1
    }),
    drivingLicense: import_pieces_framework2.Property.ShortText({
      displayName: "Driving License",
      required: !1
    }),
    drivingLicenseState: import_pieces_framework2.Property.ShortText({
      displayName: "Driving License State",
      required: !1
    }),
    isActiveMilitaryDuty: import_pieces_framework2.Property.StaticDropdown({
      displayName: "Is Active Military Duty",
      description: 'Possible values are: Yes, No. If nothing is chosen that means "Unknown".',
      required: !1,
      options: {
        disabled: !1,
        options: [
          {
            label: "Yes",
            value: !0
          },
          {
            label: "No",
            value: !1
          }
        ]
      }
    }),
    isUSCitizen: import_pieces_framework2.Property.StaticDropdown({
      displayName: "Is US Citizen",
      description: 'Possible values are: Yes, No. If nothing is chosen that means "Unknown".',
      required: !1,
      options: {
        disabled: !1,
        options: [
          {
            label: "Yes",
            value: !0
          },
          {
            label: "No",
            value: !1
          }
        ]
      }
    }),
    preferredToD: import_pieces_framework2.Property.StaticDropdown({
      displayName: "Preferred Time of Day",
      description: "Preferred Time of Day to contact with Client",
      required: !1,
      options: {
        disabled: !1,
        options: [
          {
            label: "Morning",
            value: "Morning"
          },
          {
            label: "Afternoon",
            value: "Afternoon"
          },
          {
            label: "Evening",
            value: "Evening"
          },
          {
            label: "Anytime",
            value: "Anytime"
          }
        ]
      }
    }),
    personAffiliateCode: import_pieces_framework2.Property.ShortText({
      displayName: "Person Affiliate Code",
      description: "Affiliate Code is used for the current person detection as a source of new leads. Alphanumeric characters, underscore and hyphen are allowed.",
      required: !1
    }),
    // email
    email1: import_pieces_framework2.Property.LongText({
      displayName: "Personal Email",
      description: "The contact's personal email.",
      required: !1
    }),
    email2: import_pieces_framework2.Property.LongText({
      displayName: "Alternative Personal email",
      description: "The contact's alternative personal email.",
      required: !1
    }),
    email3: import_pieces_framework2.Property.LongText({
      displayName: "Other Personal email",
      description: "The contact's additional email.",
      required: !1
    }),
    workEmail1: import_pieces_framework2.Property.LongText({
      displayName: "Work Email",
      description: "The contact's work email.",
      required: !1
    }),
    workEmail2: import_pieces_framework2.Property.LongText({
      displayName: "Alternative Work Email",
      description: "The contact's alternative work email.",
      required: !1
    }),
    workEmail3: import_pieces_framework2.Property.LongText({
      displayName: "Other Work Email",
      description: "The contact's other work email.",
      required: !1
    }),
    // phone
    mobilePhone: import_pieces_framework2.Property.ShortText({
      displayName: "Mobile Phone",
      description: "The contact's primary phone number.",
      required: !1
    }),
    mobilePhoneExt: import_pieces_framework2.Property.ShortText({
      displayName: "Mobile Phone Ext",
      description: "The contact's primary phone number extension.",
      required: !1
    }),
    homePhone: import_pieces_framework2.Property.ShortText({
      displayName: "Home Phone",
      description: "The contact's home phone number.",
      required: !1
    }),
    homePhoneExt: import_pieces_framework2.Property.ShortText({
      displayName: "Home Phone Ext",
      description: "The contact's home phone number extension.",
      required: !1
    }),
    workPhone1: import_pieces_framework2.Property.ShortText({
      displayName: "Work Phone",
      description: "The contact's work phone number.",
      required: !1
    }),
    workPhone1Ext: import_pieces_framework2.Property.ShortText({
      displayName: "Work Phone Ext",
      description: "The contact's work phone number extension.",
      required: !1
    }),
    workPhone2: import_pieces_framework2.Property.ShortText({
      displayName: "Work Phone 2",
      description: "The contact's alternative work phone number.",
      required: !1
    }),
    workPhone2Ext: import_pieces_framework2.Property.ShortText({
      displayName: "Work Phone 2 Ext",
      description: "The contact's alternative work phone number extension.",
      required: !1
    }),
    // home address
    home_street: import_pieces_framework2.Property.LongText({
      displayName: "Home Street",
      description: "The contact's full street address (can include apartment or unit number).",
      required: !1
    }),
    home_addressLine2: import_pieces_framework2.Property.LongText({
      displayName: "Address 2",
      required: !1
    }),
    home_city: import_pieces_framework2.Property.LongText({
      displayName: "City",
      description: "The contact's city of residence.",
      required: !1
    }),
    home_stateName: import_pieces_framework2.Property.ShortText({
      displayName: "State Name",
      description: "The contact's state of residence.",
      required: !1
    }),
    home_stateId: import_pieces_framework2.Property.ShortText({
      displayName: "State Code",
      description: "The contact's state code.",
      required: !1
    }),
    home_zip: import_pieces_framework2.Property.ShortText({
      displayName: "ZIP Code",
      description: "The contact's zip/postal code.",
      required: !1
    }),
    home_countryName: import_pieces_framework2.Property.ShortText({
      displayName: "Country Name",
      description: "The contact's country of residence.",
      required: !1
    }),
    home_countryId: import_pieces_framework2.Property.ShortText({
      displayName: "Country Code",
      description: "The contact's country code.",
      required: !1
    }),
    // work address
    work_street: import_pieces_framework2.Property.LongText({
      displayName: "Work Street",
      description: "The contact's work address.",
      required: !1
    }),
    work_addressLine2: import_pieces_framework2.Property.LongText({
      displayName: "Address 2",
      required: !1
    }),
    work_city: import_pieces_framework2.Property.LongText({
      displayName: "City",
      description: "The contact's work city.",
      required: !1
    }),
    work_stateName: import_pieces_framework2.Property.ShortText({
      displayName: "State Name",
      description: "The contact's work state.",
      required: !1
    }),
    work_stateId: import_pieces_framework2.Property.ShortText({
      displayName: "State Code",
      description: "The contact's work state code.",
      required: !1
    }),
    work_zip: import_pieces_framework2.Property.ShortText({
      displayName: "ZIP Code",
      description: "The contact's work zip code.",
      required: !1
    }),
    work_countryName: import_pieces_framework2.Property.ShortText({
      displayName: "Country Name",
      description: "The contact's work country.",
      required: !1
    }),
    work_countryId: import_pieces_framework2.Property.ShortText({
      displayName: "Country Code",
      description: "The contact's work country code.",
      required: !1
    }),
    // links
    webSiteUrl: import_pieces_framework2.Property.LongText({
      displayName: "Website",
      description: "The contact's company website URL.",
      required: !1
    }),
    linkedInUrl: import_pieces_framework2.Property.LongText({
      displayName: "LinkedIn",
      description: "The contact's LinkedIn profile id.",
      required: !1
    }),
    photoUrl: import_pieces_framework2.Property.LongText({
      displayName: "Photo URL",
      description: "The contact's person photo URL.",
      required: !1
    }),
    facebookUrl: import_pieces_framework2.Property.LongText({
      displayName: "Facebook",
      description: "The contact's Facebook profile id.",
      required: !1
    }),
    instagramUrl: import_pieces_framework2.Property.LongText({
      displayName: "Instagram",
      description: "The contact's Instagram profile id.",
      required: !1
    }),
    twitterUrl: import_pieces_framework2.Property.LongText({
      displayName: "Twitter",
      description: "The contact's Twitter profile id.",
      required: !1
    }),
    googlePlusUrl: import_pieces_framework2.Property.LongText({
      displayName: "Google Reviews",
      description: "The contact's Google reviews.",
      required: !1
    }),
    angelListUrl: import_pieces_framework2.Property.LongText({
      displayName: "AngelList",
      description: "The contact's AngelList profile id.",
      required: !1
    }),
    zoomUrl: import_pieces_framework2.Property.LongText({
      displayName: "Zoom",
      description: "The contact's Zoom id.",
      required: !1
    }),
    // content
    experience: import_pieces_framework2.Property.LongText({
      displayName: "Content",
      description: "The contact's professional experience.",
      required: !1
    }),
    profileSummary: import_pieces_framework2.Property.LongText({
      displayName: "Profile Summary",
      description: "The contact's profile summary.",
      required: !1
    }),
    notes: import_pieces_framework2.Property.LongText({
      displayName: "Notes",
      description: "Additional notes about the contact",
      required: !1
    }),
    followUpDate: import_pieces_framework2.Property.ShortText({
      displayName: "Follow Up Date",
      description: "Valid date format YYYY-MM-DD HH:MM:SS. If date is defined then new Follow Up Task will be created for this contact",
      required: !1
    }),
    assignedUser: import_pieces_framework2.Property.ShortText({
      displayName: "Assigned User",
      description: "Preferably, Sperse User Email should be passed as it is unique within Sperse account but User Name can be also passed",
      required: !1
    }),
    leadDealAmount: import_pieces_framework2.Property.Number({
      displayName: "Deal Amount",
      description: "Estimated deal/opportunity amount.",
      required: !1
    }),
    // custom fields
    customField1: import_pieces_framework2.Property.LongText({
      displayName: "Custom Field 1",
      description: "Additional custom data for the contact record.",
      required: !1
    }),
    customField2: import_pieces_framework2.Property.LongText({
      displayName: "Custom Field 2",
      required: !1
    }),
    customField3: import_pieces_framework2.Property.LongText({
      displayName: "Custom Field 3",
      required: !1
    }),
    customField4: import_pieces_framework2.Property.LongText({
      displayName: "Custom Field 4",
      required: !1
    }),
    customField5: import_pieces_framework2.Property.LongText({
      displayName: "Custom Field 5",
      required: !1
    }),
    // business info
    companyName: import_pieces_framework2.Property.LongText({
      displayName: "Company Name",
      description: "Name of the contact's company (This field is mandatory if the First Name and Last Name fields are empty).",
      required: !1
    }),
    organizationType: import_pieces_framework2.Property.StaticDropdown({
      displayName: "Organization Type",
      required: !1,
      options: {
        disabled: !1,
        options: [
          {
            label: "LLP",
            value: "LLP"
          },
          {
            label: "LLC",
            value: "LLC"
          },
          {
            label: "Inc",
            value: "Inc"
          },
          {
            label: "LP",
            value: "LP"
          },
          {
            label: "Partnership",
            value: "Partnership"
          },
          {
            label: "Sole Proprietership",
            value: "Sole Proprietership"
          },
          {
            label: "Trust",
            value: "Trust"
          },
          {
            label: "LLLP",
            value: "LLLP"
          },
          {
            label: "Other",
            value: "Other"
          }
        ]
      }
    }),
    isEmployed: import_pieces_framework2.Property.StaticDropdown({
      displayName: "Is Employed",
      description: "Pass yes if the client is employed in this Organization.",
      required: !1,
      options: {
        disabled: !1,
        options: [
          {
            label: "Yes",
            value: !0
          },
          {
            label: "No",
            value: !1
          }
        ]
      }
    }),
    jobTitle: import_pieces_framework2.Property.ShortText({
      displayName: "Job Title",
      description: "The contact's job title.",
      required: !1
    }),
    employmentStartDate: import_pieces_framework2.Property.ShortText({
      displayName: "Employment Start Date",
      description: "Valid date format YYYY-MM-DD or MM-DD-YYYY",
      required: !1
    }),
    employeeCount: import_pieces_framework2.Property.ShortText({
      displayName: "Employee Count",
      required: !1
    }),
    dateFounded: import_pieces_framework2.Property.ShortText({
      displayName: "Date Founded",
      description: "Valid date format YYYY-MM-DD or MM-DD-YYYY",
      required: !1
    }),
    ein: import_pieces_framework2.Property.LongText({
      displayName: "EIN",
      required: !1
    }),
    annualRevenue: import_pieces_framework2.Property.Number({
      displayName: "Annual Revenue",
      required: !1
    }),
    industry: import_pieces_framework2.Property.ShortText({
      displayName: "Industry",
      description: "The company's industry.",
      required: !1
    }),
    companyPhone: import_pieces_framework2.Property.ShortText({
      displayName: "Company Phone",
      required: !1
    }),
    companyPhoneExt: import_pieces_framework2.Property.ShortText({
      displayName: "Company Phone Extension",
      required: !1
    }),
    companyFaxNumber: import_pieces_framework2.Property.LongText({
      displayName: "Company Fax Number",
      required: !1
    }),
    companyEmail: import_pieces_framework2.Property.LongText({
      displayName: "Company Email",
      required: !1
    }),
    companyWebSiteUrl: import_pieces_framework2.Property.LongText({
      displayName: "Company Website",
      required: !1
    }),
    companyFacebookUrl: import_pieces_framework2.Property.LongText({
      displayName: "Company Facebook",
      required: !1
    }),
    companyLinkedInUrl: import_pieces_framework2.Property.LongText({
      displayName: "Company LinkedIn",
      required: !1
    }),
    companyInstagramUrl: import_pieces_framework2.Property.LongText({
      displayName: "Company Instagram",
      required: !1
    }),
    companyTwitterUrl: import_pieces_framework2.Property.LongText({
      displayName: "Company Twitter",
      required: !1
    }),
    companyGooglePlusUrl: import_pieces_framework2.Property.LongText({
      displayName: "Company Google Reviews",
      required: !1
    }),
    companyCrunchbaseUrl: import_pieces_framework2.Property.LongText({
      displayName: "Company Crunchbase",
      required: !1
    }),
    companyBBBUrl: import_pieces_framework2.Property.LongText({
      displayName: "Company BBB URL",
      required: !1
    }),
    companyZoomUrl: import_pieces_framework2.Property.LongText({
      displayName: "Company Zoom",
      required: !1
    }),
    companyCalendlyUrl: import_pieces_framework2.Property.LongText({
      displayName: "Company Calendly",
      required: !1
    }),
    companyLogoUrl: import_pieces_framework2.Property.LongText({
      displayName: "Company Logo URL",
      required: !1
    }),
    companyAffiliateCode: import_pieces_framework2.Property.ShortText({
      displayName: "Company Affiliate Code",
      required: !1
    }),
    // company full Address
    company_street: import_pieces_framework2.Property.LongText({
      displayName: "Company Street",
      required: !1
    }),
    company_addressLine2: import_pieces_framework2.Property.LongText({
      displayName: "Address 2",
      required: !1
    }),
    company_city: import_pieces_framework2.Property.ShortText({
      displayName: "City",
      required: !1
    }),
    company_stateName: import_pieces_framework2.Property.ShortText({
      displayName: "State Name",
      required: !1
    }),
    company_stateId: import_pieces_framework2.Property.ShortText({
      displayName: "State Code",
      required: !1
    }),
    company_zip: import_pieces_framework2.Property.ShortText({
      displayName: "Company Zip Code",
      required: !1
    }),
    company_countryName: import_pieces_framework2.Property.LongText({
      displayName: "Company Country Name",
      required: !1
    }),
    company_countryId: import_pieces_framework2.Property.ShortText({
      displayName: "Company Country Code",
      required: !1
    }),
    // tracking info
    leadSource: import_pieces_framework2.Property.ShortText({
      displayName: "Source Code",
      description: "The first known source the contact used to find your website. You can set this automatically and update manually later.",
      required: !1
    }),
    channelId: import_pieces_framework2.Property.ShortText({
      displayName: "Channel Code",
      description: "The channel/medium the contact used to find your website.",
      required: !1
    }),
    affiliateCode: import_pieces_framework2.Property.ShortText({
      displayName: "Affiliate Code",
      description: "The affiliate/referer partner through which the contact signed up.",
      required: !1
    }),
    campaignId: import_pieces_framework2.Property.ShortText({
      displayName: "Campaign ID",
      required: !1
    }),
    gclId: import_pieces_framework2.Property.ShortText({
      displayName: "Google Click ID",
      required: !1
    }),
    refererURL: import_pieces_framework2.Property.LongText({
      displayName: "Referer URL",
      description: "The webpage where the contact clicked a link that sent them to your website.",
      required: !1
    }),
    applicantId: import_pieces_framework2.Property.ShortText({
      displayName: "Applicant ID",
      required: !1
    }),
    applicationId: import_pieces_framework2.Property.ShortText({
      displayName: "Application ID",
      required: !1
    }),
    ipAddress: import_pieces_framework2.Property.LongText({
      displayName: "IP Address",
      required: !1
    }),
    userAgent: import_pieces_framework2.Property.ShortText({
      displayName: "User Agent",
      required: !1
    }),
    siteId: import_pieces_framework2.Property.ShortText({
      displayName: "Site ID",
      required: !1
    }),
    siteUrl: import_pieces_framework2.Property.LongText({
      displayName: "Site URL",
      required: !1
    }),
    dateCreated: import_pieces_framework2.Property.ShortText({
      displayName: "Date Created",
      description: "Valid date format YYYY-MM-DD HH:MM:SS",
      required: !1
    }),
    entryUrl: import_pieces_framework2.Property.LongText({
      displayName: "Entry URL",
      description: "The first page of visit through which the contact visited your website.",
      required: !1
    }),
    // contact Attributes
    leadStageName: import_pieces_framework2.Property.ShortText({
      displayName: "Lead Stage Name",
      description: "The first page of visit through which the contact visited your website.",
      required: !1
    }),
    star: import_pieces_framework2.Property.ShortText({
      displayName: "Star",
      description: "String Value. Supports the following items: Yellow, Blue, Green, Purple, Red. Other values will be skipped.",
      required: !1
    }),
    rating: import_pieces_framework2.Property.ShortText({
      displayName: "Rating",
      description: "This is the static array from 1 to 10",
      required: !1
    }),
    // UtmParameters
    utmSource: import_pieces_framework2.Property.LongText({
      displayName: "UTM Source",
      required: !1
    }),
    utmMedium: import_pieces_framework2.Property.LongText({
      displayName: "UTM Medium",
      required: !1
    }),
    utmCampaign: import_pieces_framework2.Property.LongText({
      displayName: "UTM Campaign",
      required: !1
    }),
    utmTerm: import_pieces_framework2.Property.LongText({
      displayName: "UTM Term",
      required: !1
    }),
    utmContent: import_pieces_framework2.Property.LongText({
      displayName: "UTM Content",
      required: !1
    }),
    utmKeyword: import_pieces_framework2.Property.LongText({
      displayName: "UTM Keyword",
      required: !1
    }),
    utmAdGroup: import_pieces_framework2.Property.LongText({
      displayName: "UTM AdGroup",
      required: !1
    }),
    utmName: import_pieces_framework2.Property.LongText({
      displayName: "UTM Name",
      required: !1
    }),
    // requestCustomInfo
    requestCustomField1: import_pieces_framework2.Property.LongText({
      displayName: "Request Custom Field 1",
      required: !1
    }),
    requestCustomField2: import_pieces_framework2.Property.LongText({
      displayName: "Request Custom Field 2",
      required: !1
    }),
    requestCustomField3: import_pieces_framework2.Property.LongText({
      displayName: "Request Custom Field 3",
      required: !1
    }),
    requestCustomField4: import_pieces_framework2.Property.LongText({
      displayName: "Request Custom Field 4",
      required: !1
    }),
    requestCustomField5: import_pieces_framework2.Property.LongText({
      displayName: "Request Custom Field 5",
      required: !1
    }),
    // subscription1
    sub1_productCode: import_pieces_framework2.Property.ShortText({
      displayName: "Product Code",
      description: "Product Code from the Sperse CRM. Either Product Code and Payment Period Type or System Type and Code are required fields within Subscription 1.",
      required: !1
    }),
    sub1_paymentPeriodType: import_pieces_framework2.Property.StaticDropdown({
      displayName: "Product Payment Period Type",
      description: "The chosen Period Type has to be set for the Product on Sperse side",
      required: !1,
      options: {
        disabled: !1,
        options: [
          {
            label: "Monthly",
            value: "Monthly"
          },
          {
            label: "Annual",
            value: "Annual"
          },
          {
            label: "LifeTime",
            value: "LifeTime"
          }
        ]
      }
    }),
    sub1_systemType: import_pieces_framework2.Property.ShortText({
      displayName: "System Type",
      description: "Either Product Code and Payment Period Type or System Type and Code are required fields within Subscription 1.",
      required: !1
    }),
    sub1_code: import_pieces_framework2.Property.ShortText({
      displayName: "Code",
      description: "Code of subscription service from the Sperse CRM. Either Product Code and Payment Period Type or System Type and Code are required fields within Subscription 1.",
      required: !1
    }),
    sub1_name: import_pieces_framework2.Property.ShortText({
      displayName: "Name",
      required: !1
    }),
    sub1_level: import_pieces_framework2.Property.ShortText({
      displayName: "Level",
      description: "Code of subscription service level from the Sperse CRM.",
      required: !1
    }),
    sub1_startDate: import_pieces_framework2.Property.ShortText({
      displayName: "Start Date",
      description: "Valid date format YYYY-MM-DD HH:MM:SS.",
      required: !1
    }),
    sub1_endDate: import_pieces_framework2.Property.ShortText({
      displayName: "End Date",
      description: "Valid date format YYYY-MM-DD HH:MM:SS.",
      required: !1
    }),
    sub1_amount: import_pieces_framework2.Property.Number({
      displayName: "Amount",
      required: !1
    }),
    // subscription2
    sub2_productCode: import_pieces_framework2.Property.ShortText({
      displayName: "Product Code",
      description: "Product Code from the Sperse CRM. Either Product Code and Payment Period Type or System Type and Code are required fields within Subscription 2.",
      required: !1
    }),
    sub2_paymentPeriodType: import_pieces_framework2.Property.StaticDropdown({
      displayName: "Product Payment Period Type",
      description: "The chosen Period Type has to be set for the Product on Sperse side",
      required: !1,
      options: {
        disabled: !1,
        options: [
          {
            label: "Monthly",
            value: "Monthly"
          },
          {
            label: "Annual",
            value: "Annual"
          },
          {
            label: "LifeTime",
            value: "LifeTime"
          }
        ]
      }
    }),
    sub2_systemType: import_pieces_framework2.Property.ShortText({
      displayName: "System Type",
      description: "Either Product Code and Payment Period Type or System Type and Code are required fields within Subscription 2.",
      required: !1
    }),
    sub2_code: import_pieces_framework2.Property.ShortText({
      displayName: "Code",
      description: "Code of subscription service from the Sperse CRM. Either Product Code and Payment Period Type or System Type and Code are required fields within Subscription 2.",
      required: !1
    }),
    sub2_name: import_pieces_framework2.Property.ShortText({
      displayName: "Name",
      required: !1
    }),
    sub2_level: import_pieces_framework2.Property.ShortText({
      displayName: "Level",
      description: "Code of subscription service level from the Sperse CRM.",
      required: !1
    }),
    sub2_startDate: import_pieces_framework2.Property.ShortText({
      displayName: "Start Date",
      description: "Valid date format YYYY-MM-DD HH:MM:SS.",
      required: !1
    }),
    sub2_endDate: import_pieces_framework2.Property.ShortText({
      displayName: "End Date",
      description: "Valid date format YYYY-MM-DD HH:MM:SS.",
      required: !1
    }),
    sub2_amount: import_pieces_framework2.Property.Number({
      displayName: "Amount",
      required: !1
    }),
    // subscription3
    sub3_productCode: import_pieces_framework2.Property.ShortText({
      displayName: "Product Code",
      description: "Product Code from the Sperse CRM. Either Product Code and Payment Period Type or System Type and Code are required fields within Subscription 3.",
      required: !1
    }),
    sub3_paymentPeriodType: import_pieces_framework2.Property.StaticDropdown({
      displayName: "Product Payment Period Type",
      description: "The chosen Period Type has to be set for the Product on Sperse side",
      required: !1,
      options: {
        disabled: !1,
        options: [
          {
            label: "Monthly",
            value: "Monthly"
          },
          {
            label: "Annual",
            value: "Annual"
          },
          {
            label: "LifeTime",
            value: "LifeTime"
          }
        ]
      }
    }),
    sub3_systemType: import_pieces_framework2.Property.ShortText({
      displayName: "System Type",
      description: "Either Product Code and Payment Period Type or System Type and Code are required fields within Subscription 3.",
      required: !1
    }),
    sub3_code: import_pieces_framework2.Property.ShortText({
      displayName: "Code",
      description: "Code of subscription service from the Sperse CRM. Either Product Code and Payment Period Type or System Type and Code are required fields within Subscription 3.",
      required: !1
    }),
    sub3_name: import_pieces_framework2.Property.ShortText({
      displayName: "Name",
      required: !1
    }),
    sub3_level: import_pieces_framework2.Property.ShortText({
      displayName: "Level",
      description: "Code of subscription service level from the Sperse CRM.",
      required: !1
    }),
    sub3_startDate: import_pieces_framework2.Property.ShortText({
      displayName: "Start Date",
      description: "Valid date format YYYY-MM-DD HH:MM:SS.",
      required: !1
    }),
    sub3_endDate: import_pieces_framework2.Property.ShortText({
      displayName: "End Date",
      description: "Valid date format YYYY-MM-DD HH:MM:SS.",
      required: !1
    }),
    sub3_amount: import_pieces_framework2.Property.Number({
      displayName: "Amount",
      required: !1
    })
  },
  async run(context) {
    let customer = {
      importType: context.propsValue.importType,
      ignoreInvalidValues: context.propsValue.ignoreInvalidValues,
      matchExisting: context.propsValue.matchExisting,
      overrideLists: context.propsValue.overrideLists,
      createUser: context.propsValue.createUser,
      sendWelcomeEmail: context.propsValue.sendWelcomeEmail,
      contactId: context.propsValue.contactId,
      contactXref: context.propsValue.contactXref,
      userPassword: context.propsValue.userPassword,
      personalInfo: {
        fullName: {
          namePrefix: context.propsValue.namePrefix,
          firstName: context.propsValue.firstName,
          middleName: context.propsValue.middleName,
          lastName: context.propsValue.lastName,
          nameSuffix: context.propsValue.nickName,
          nickName: context.propsValue.nameSuffix
        },
        doB: context.propsValue.dob,
        mobilePhone: context.propsValue.mobilePhone,
        mobilePhoneExt: context.propsValue.mobilePhoneExt,
        homePhone: context.propsValue.homePhone,
        homePhoneExt: context.propsValue.homePhoneExt,
        ssn: context.propsValue.ssn,
        bankCode: context.propsValue.bankCode,
        email1: context.propsValue.email1,
        email2: context.propsValue.email2,
        email3: context.propsValue.email3,
        preferredToD: context.propsValue.preferredToD,
        drivingLicense: context.propsValue.drivingLicense,
        drivingLicenseState: context.propsValue.drivingLicenseState,
        isActiveMilitaryDuty: context.propsValue.isActiveMilitaryDuty,
        gender: context.propsValue.gender,
        fullAddress: {
          street: context.propsValue.home_street,
          addressLine2: context.propsValue.home_addressLine2,
          city: context.propsValue.home_city,
          stateName: context.propsValue.home_stateName,
          stateId: context.propsValue.home_stateId,
          zip: context.propsValue.home_zip,
          countryName: context.propsValue.home_countryName,
          countryId: context.propsValue.home_countryId
        },
        isUSCitizen: context.propsValue.isUSCitizen,
        webSiteUrl: context.propsValue.webSiteUrl,
        facebookUrl: context.propsValue.facebookUrl,
        linkedInUrl: context.propsValue.linkedInUrl,
        instagramUrl: context.propsValue.instagramUrl,
        twitterUrl: context.propsValue.twitterUrl,
        googlePlusUrl: context.propsValue.googlePlusUrl,
        angelListUrl: context.propsValue.angelListUrl,
        zoomUrl: context.propsValue.zoomUrl,
        photoUrl: context.propsValue.photoUrl,
        experience: context.propsValue.experience,
        profileSummary: context.propsValue.profileSummary,
        affiliateCode: context.propsValue.personAffiliateCode,
        customFields: {
          customField1: context.propsValue.customField1,
          customField2: context.propsValue.customField2,
          customField3: context.propsValue.customField3,
          customField4: context.propsValue.customField4,
          customField5: context.propsValue.customField5
        }
      },
      businessInfo: {
        companyName: context.propsValue.companyName,
        organizationType: context.propsValue.organizationType,
        jobTitle: context.propsValue.isEmployed,
        isEmployed: context.propsValue.jobTitle,
        employmentStartDate: context.propsValue.employmentStartDate,
        employeeCount: context.propsValue.employeeCount,
        dateFounded: context.propsValue.dateFounded,
        ein: context.propsValue.ein,
        annualRevenue: context.propsValue.annualRevenue,
        industry: context.propsValue.industry,
        companyPhone: context.propsValue.companyPhone,
        companyPhoneExt: context.propsValue.companyPhoneExt,
        companyFaxNumber: context.propsValue.companyFaxNumber,
        companyEmail: context.propsValue.companyEmail,
        companyFullAddress: {
          street: context.propsValue.company_street,
          addressLine2: context.propsValue.company_addressLine2,
          city: context.propsValue.company_city,
          stateName: context.propsValue.company_stateName,
          stateId: context.propsValue.company_stateId,
          zip: context.propsValue.company_zip,
          countryName: context.propsValue.company_countryName,
          countryId: context.propsValue.company_countryId
        },
        companyWebSiteUrl: context.propsValue.companyWebSiteUrl,
        companyFacebookUrl: context.propsValue.companyFacebookUrl,
        companyLinkedInUrl: context.propsValue.companyLinkedInUrl,
        companyInstagramUrl: context.propsValue.companyInstagramUrl,
        companyTwitterUrl: context.propsValue.companyTwitterUrl,
        companyGooglePlusUrl: context.propsValue.companyGooglePlusUrl,
        companyCrunchbaseUrl: context.propsValue.companyCrunchbaseUrl,
        companyBBBUrl: context.propsValue.companyBBBUrl,
        companyCalendlyUrl: context.propsValue.companyZoomUrl,
        companyZoomUrl: context.propsValue.companyCalendlyUrl,
        companyLogoUrl: context.propsValue.companyLogoUrl,
        workPhone1: context.propsValue.workPhone1,
        workPhone1Ext: context.propsValue.workPhone1Ext,
        workPhone2: context.propsValue.workPhone2,
        workPhone2Ext: context.propsValue.workPhone2Ext,
        workEmail1: context.propsValue.workEmail1,
        workEmail2: context.propsValue.workEmail2,
        workEmail3: context.propsValue.workEmail3,
        workFullAddress: {
          street: context.propsValue.work_street,
          addressLine2: context.propsValue.work_addressLine2,
          city: context.propsValue.work_city,
          stateName: context.propsValue.work_stateName,
          stateId: context.propsValue.work_stateId,
          zip: context.propsValue.work_zip,
          countryName: context.propsValue.work_countryName,
          countryId: context.propsValue.work_countryId
        },
        affiliateCode: context.propsValue.companyAffiliateCode
      },
      assignedUser: context.propsValue.assignedUser,
      followUpDate: context.propsValue.followUpDate,
      notes: context.propsValue.notes,
      dateCreated: context.propsValue.dateCreated,
      leadStageName: context.propsValue.leadStageName,
      leadSource: context.propsValue.leadSource,
      leadDealAmount: context.propsValue.leadDealAmount,
      affiliateCode: context.propsValue.affiliateCode,
      campaignId: context.propsValue.campaignId,
      channelId: context.propsValue.channelId,
      gclId: context.propsValue.gclId,
      refererUrl: context.propsValue.refererURL,
      entryUrl: context.propsValue.entryUrl,
      applicantId: context.propsValue.applicantId,
      applicationId: context.propsValue.applicationId,
      ipAddress: context.propsValue.ipAddress,
      userAgent: context.propsValue.userAgent,
      siteId: context.propsValue.siteId,
      siteUrl: context.propsValue.siteUrl,
      utmSource: context.propsValue.utmSource,
      utmMedium: context.propsValue.utmMedium,
      utmCampaign: context.propsValue.utmCampaign,
      utmTerm: context.propsValue.utmTerm,
      utmContent: context.propsValue.utmContent,
      utmKeyword: context.propsValue.utmKeyword,
      utmAdGroup: context.propsValue.utmAdGroup,
      utmName: context.propsValue.utmName,
      requestCustomInfo: {
        customField1: context.propsValue.requestCustomField1,
        customField2: context.propsValue.requestCustomField2,
        customField3: context.propsValue.requestCustomField3,
        customField4: context.propsValue.requestCustomField4,
        customField5: context.propsValue.requestCustomField5
      },
      subscription1: {
        productCode: context.propsValue.sub1_productCode,
        paymentPeriodType: context.propsValue.sub1_paymentPeriodType,
        systemType: context.propsValue.sub1_systemType,
        code: context.propsValue.sub1_code,
        name: context.propsValue.sub1_name,
        level: context.propsValue.sub1_level,
        startDate: context.propsValue.sub1_startDate,
        endDate: context.propsValue.sub1_endDate,
        amount: context.propsValue.sub1_amount
      },
      subscription2: {
        productCode: context.propsValue.sub2_productCode,
        paymentPeriodType: context.propsValue.sub2_paymentPeriodType,
        systemType: context.propsValue.sub2_systemType,
        code: context.propsValue.sub2_code,
        name: context.propsValue.sub2_name,
        level: context.propsValue.sub2_level,
        startDate: context.propsValue.sub2_startDate,
        endDate: context.propsValue.sub2_endDate,
        amount: context.propsValue.sub2_amount
      },
      subscription3: {
        productCode: context.propsValue.sub3_productCode,
        paymentPeriodType: context.propsValue.sub3_paymentPeriodType,
        systemType: context.propsValue.sub3_systemType,
        code: context.propsValue.sub3_code,
        name: context.propsValue.sub3_name,
        level: context.propsValue.sub3_level,
        startDate: context.propsValue.sub3_startDate,
        endDate: context.propsValue.sub3_endDate,
        amount: context.propsValue.sub3_amount
      },
      classificationInfo: {
        rating: context.propsValue.rating
      }
    }, res = await import_pieces_common.httpClient.sendRequest({
      method: import_pieces_common.HttpMethod.POST,
      url: `${context.auth.props.base_url}/api/services/CRM/Import/ImportContact`,
      headers: {
        "api-key": context.auth.props.api_key,
        // Pass API key in headers
        "Content-Type": "application/json"
      },
      body: __spreadValues({}, customer)
    });
    return {
      status: res.status,
      body: res.body
    };
  }
});

// ../ap/packages/pieces/community/upgradechat/src/lib/actions/add-or-update-contact.ts
var import_pieces_framework3 = require("@activepieces/pieces-framework");
var import_pieces_common2 = require("@activepieces/pieces-common");
var addOrUpdateContact = (0, import_pieces_framework3.createAction)({
  name: "addOrUpdateContact",
  displayName: "Add or Update Contact",
  description: "Creates a new contact.",
  audience: "both",
  aiMetadata: {
    description: "Imports a contact (Lead, Client, or Partner) into the Sperse/Upgrade.chat CRM. When 'Match Existing Contact' is enabled (the default), it looks up an existing record by email and full name and updates it instead of creating a duplicate, making it an upsert; with matching disabled it always inserts a new contact. Choose this for basic contact records; use the extended variant when you need company, address, subscription, or tracking detail. First or Last name is required (or a Company Name). Idempotent only while matching is enabled and the same email/name is supplied.",
    idempotent: !0
  },
  auth: upgradechatAuth,
  props: {
    importType: import_pieces_framework3.Property.StaticDropdown({
      displayName: "Contact Type",
      required: !0,
      defaultValue: "Lead",
      options: {
        disabled: !1,
        options: [
          {
            label: "Lead",
            value: "Lead"
          },
          {
            label: "Client",
            value: "Client"
          },
          {
            label: "Partner",
            value: "Partner"
          }
        ]
      }
    }),
    matchExisting: import_pieces_framework3.Property.StaticDropdown({
      displayName: "Match Existing Contact",
      description: 'If "Yes", will try to find an existing record using Email and Full Name and update it.',
      required: !1,
      defaultValue: !0,
      options: {
        disabled: !1,
        options: [
          {
            label: "Yes",
            value: !0
          },
          {
            label: "No",
            value: !1
          }
        ]
      }
    }),
    contactId: import_pieces_framework3.Property.Number({
      displayName: "Contact ID",
      description: "Sperse Contact ID. Will be used for looking a client",
      defaultValue: 0,
      required: !1
    }),
    // fullname
    fullName: import_pieces_framework3.Property.ShortText({
      displayName: "Full Name",
      description: "The contact's full name.",
      required: !1
    }),
    namePrefix: import_pieces_framework3.Property.ShortText({
      displayName: "Prefix",
      description: "The title used to address the contact.",
      required: !1
    }),
    firstName: import_pieces_framework3.Property.ShortText({
      displayName: "First Name",
      description: "Required if Last Name and Company Name fields are empty.",
      required: !0
    }),
    middleName: import_pieces_framework3.Property.ShortText({
      displayName: "Middle Name",
      description: "The contact's middle name.",
      required: !1
    }),
    lastName: import_pieces_framework3.Property.ShortText({
      displayName: "Last Name",
      description: "Required if First Name and Company Name fields are empty.",
      required: !0
    }),
    nickName: import_pieces_framework3.Property.ShortText({
      displayName: "Nick Name",
      description: "The contact's nick name.",
      required: !1
    }),
    nameSuffix: import_pieces_framework3.Property.ShortText({
      displayName: "Suffix",
      description: "Additional information about the contact e.g PhD.",
      required: !1
    }),
    // personal info
    gender: import_pieces_framework3.Property.StaticDropdown({
      displayName: "Gender",
      required: !1,
      options: {
        disabled: !1,
        options: [
          {
            label: "Male",
            value: "Male"
          },
          {
            label: "Female",
            value: "Female"
          }
        ]
      }
    }),
    dob: import_pieces_framework3.Property.ShortText({
      displayName: "Date of Birth",
      description: "Valid date format YYYY-MM-DD or MM-DD-YYYY.",
      required: !1
    }),
    bankCode: import_pieces_framework3.Property.ShortText({
      displayName: "Bank Code",
      description: "The contact's 4-letter personality code.",
      required: !1
    }),
    ssn: import_pieces_framework3.Property.ShortText({
      displayName: "SSN",
      description: "The contact's social security number.",
      required: !1
    }),
    // business info
    companyName: import_pieces_framework3.Property.ShortText({
      displayName: "Company Name",
      description: "Name of the contact's company (This field is mandatory if the First Name and Last Name fields are empty).",
      required: !1
    }),
    jobTitle: import_pieces_framework3.Property.ShortText({
      displayName: "Job Title",
      description: "The contact's job title.",
      required: !1
    }),
    industry: import_pieces_framework3.Property.ShortText({
      displayName: "Industry",
      description: "The company's industry.",
      required: !1
    }),
    // email
    workEmail1: import_pieces_framework3.Property.LongText({
      displayName: "Work Email",
      description: "The contact's work email.",
      required: !1
    }),
    email1: import_pieces_framework3.Property.LongText({
      displayName: "Personal Email",
      description: "The contact's personal email.",
      required: !1
    }),
    email2: import_pieces_framework3.Property.LongText({
      displayName: "Other email",
      description: "The contact's additional email.",
      required: !1
    }),
    // phone
    workPhone1: import_pieces_framework3.Property.ShortText({
      displayName: "Work Phone",
      description: "The contact's work/primary phone number.",
      required: !1
    }),
    homePhone: import_pieces_framework3.Property.ShortText({
      displayName: "Home Phone",
      description: "The contact's home phone number.",
      required: !1
    }),
    mobilePhone: import_pieces_framework3.Property.ShortText({
      displayName: "Mobile Phone",
      description: "The contact's mobile phone number.",
      required: !1
    }),
    // links
    webSiteUrl: import_pieces_framework3.Property.ShortText({
      displayName: "Website",
      description: "The contact's company website URL.",
      required: !1
    }),
    linkedInUrl: import_pieces_framework3.Property.ShortText({
      displayName: "LinkedIn",
      description: "The contact's LinkedIn profile id.",
      required: !1
    }),
    photoUrl: import_pieces_framework3.Property.ShortText({
      displayName: "Photo URL",
      description: "The contact's person photo URL.",
      required: !1
    }),
    // Full Address
    street: import_pieces_framework3.Property.ShortText({
      displayName: "Street",
      description: "The contact's full street address (can include apartment or unit number).",
      required: !1
    }),
    addressLine2: import_pieces_framework3.Property.ShortText({
      displayName: "Address 2",
      required: !1
    }),
    city: import_pieces_framework3.Property.ShortText({
      displayName: "City",
      description: "The contact's city of residence.",
      required: !1
    }),
    stateName: import_pieces_framework3.Property.ShortText({
      displayName: "State Name",
      description: "The contact's state of residence.",
      required: !1
    }),
    stateId: import_pieces_framework3.Property.ShortText({
      displayName: "State Code",
      description: "The contact's state code.",
      required: !1
    }),
    zip: import_pieces_framework3.Property.ShortText({
      displayName: "Zip Code",
      description: "The contact's zip/postal code.",
      required: !1
    }),
    countryName: import_pieces_framework3.Property.ShortText({
      displayName: "Country Name",
      description: "The contact's country of residence.",
      required: !1
    }),
    countryId: import_pieces_framework3.Property.ShortText({
      displayName: "Country Code",
      description: "The contact's country code.",
      required: !1
    }),
    // content
    experience: import_pieces_framework3.Property.LongText({
      displayName: "Content",
      description: "The contact's professional experience.",
      required: !1
    }),
    profileSummary: import_pieces_framework3.Property.LongText({
      displayName: "Profile Summary",
      description: "The contact's profile summary.",
      required: !1
    }),
    notes: import_pieces_framework3.Property.LongText({
      displayName: "notes",
      description: "Additional notes about the contact",
      required: !1
    }),
    followUpDate: import_pieces_framework3.Property.ShortText({
      displayName: "Follow Up Date",
      description: "Valid date format YYYY-MM-DD HH:MM:SS. If date is defined then new Follow Up Task will be created for this contact",
      required: !1
    }),
    assignedUser: import_pieces_framework3.Property.ShortText({
      displayName: "Assigned User",
      description: "Preferably, Sperse User Email should be passed as it is unique within Sperse account but User Name can be also passed",
      required: !1
    }),
    leadDealAmount: import_pieces_framework3.Property.Number({
      displayName: "Deal Amount",
      description: "Estimated deal/opportunity amount.",
      required: !1
    }),
    // tracking info
    leadSource: import_pieces_framework3.Property.ShortText({
      displayName: "Source Code",
      description: "The first known source the contact used to find your website. You can set this automatically and update manually later.",
      required: !1
    }),
    channelId: import_pieces_framework3.Property.ShortText({
      displayName: "Channel Code",
      description: "The channel/medium the contact used to find your website.",
      required: !1
    }),
    affiliateCode: import_pieces_framework3.Property.ShortText({
      displayName: "Affiliate Code",
      description: "The affiliate/referer partner through which the contact signed up.",
      required: !1
    }),
    refererURL: import_pieces_framework3.Property.ShortText({
      displayName: "Referer URL",
      description: "The webpage where the contact clicked a link that sent them to your website.",
      required: !1
    }),
    entryUrl: import_pieces_framework3.Property.ShortText({
      displayName: "Entry URL",
      description: "The first page of visit through which the contact visited your website.",
      required: !1
    })
  },
  async run(context) {
    let contact = {
      importType: context.propsValue.importType,
      matchExisting: context.propsValue.matchExisting,
      contactId: context.propsValue.contactId,
      personalInfo: {
        fullName: {
          namePrefix: context.propsValue.namePrefix,
          firstName: context.propsValue.firstName,
          middleName: context.propsValue.middleName,
          lastName: context.propsValue.lastName,
          nameSuffix: context.propsValue.nameSuffix,
          nickName: context.propsValue.nickName
        },
        doB: context.propsValue.dob,
        mobilePhone: context.propsValue.mobilePhone,
        homePhone: context.propsValue.homePhone,
        ssn: context.propsValue.ssn,
        bankCode: context.propsValue.bankCode,
        email1: context.propsValue.email1,
        email2: context.propsValue.email2,
        gender: context.propsValue.gender,
        fullAddress: {
          street: context.propsValue.street,
          addressLine2: context.propsValue.addressLine2,
          city: context.propsValue.city,
          stateName: context.propsValue.stateName,
          stateId: context.propsValue.stateId,
          zip: context.propsValue.zip,
          countryName: context.propsValue.countryName,
          countryId: context.propsValue.countryId
        },
        webSiteUrl: context.propsValue.webSiteUrl,
        linkedInUrl: context.propsValue.linkedInUrl,
        photoUrl: context.propsValue.photoUrl,
        experience: context.propsValue.experience,
        profileSummary: context.propsValue.profileSummary
      },
      businessInfo: {
        companyName: context.propsValue.companyName,
        jobTitle: context.propsValue.jobTitle,
        industry: context.propsValue.industry,
        workPhone1: context.propsValue.workPhone1,
        workEmail1: context.propsValue.workEmail1
      },
      assignedUser: context.propsValue.assignedUser,
      followUpDate: context.propsValue.followUpDate,
      notes: context.propsValue.notes,
      leadDealAmount: context.propsValue.leadDealAmount,
      leadSource: context.propsValue.leadSource,
      channelId: context.propsValue.channelId,
      affiliateCode: context.propsValue.affiliateCode,
      refererUrl: context.propsValue.refererURL,
      entryUrl: context.propsValue.entryUrl
    }, res = await import_pieces_common2.httpClient.sendRequest({
      method: import_pieces_common2.HttpMethod.POST,
      url: `${context.auth.props.base_url}/api/services/CRM/Import/ImportContact`,
      headers: {
        "api-key": context.auth.props.api_key,
        // Pass API key in headers
        "Content-Type": "application/json"
      },
      body: __spreadValues({}, contact)
    });
    return {
      status: res.status,
      body: res.body
    };
  }
});

// ../ap/packages/pieces/community/upgradechat/src/lib/actions/add-or-update-subscription.ts
var import_pieces_framework4 = require("@activepieces/pieces-framework");
var import_pieces_common3 = require("@activepieces/pieces-common");
var addOrUpdateSubscription = (0, import_pieces_framework4.createAction)({
  name: "addOrUpdateSubscription",
  displayName: "Add or Update Subscription",
  description: "Creates a new subscription.",
  audience: "both",
  aiMetadata: {
    description: "Creates or updates a subscription for a contact in the Sperse/Upgrade.chat CRM, linking the given product (by Product Code, required) at a chosen payment period (Monthly, Annual, or LifeTime) with optional recurring billing. The contact must be identifiable by Contact ID or External Contact ID (Contact XREF). Choose this to attach a subscription/plan to an existing contact. The underlying update is an upsert keyed on the contact and product, so repeating the same call converges to the same subscription state rather than stacking duplicates.",
    idempotent: !0
  },
  auth: upgradechatAuth,
  props: {
    contactId: import_pieces_framework4.Property.Number({
      displayName: "Contact ID",
      defaultValue: 0,
      required: !1
    }),
    productId: import_pieces_framework4.Property.Number({
      displayName: "Product ID",
      required: !1
    }),
    contactXref: import_pieces_framework4.Property.ShortText({
      displayName: "External Contact ID",
      description: "ContactXref have to be specified and correct to look up the correct contact",
      required: !1
    }),
    productCode: import_pieces_framework4.Property.ShortText({
      displayName: "Product Code",
      description: "Product Code (Unique product identifier). ProductCode have to be specified and correct to look up the correct product",
      required: !0
    }),
    paymentPeriodType: import_pieces_framework4.Property.StaticDropdown({
      displayName: "Payment Period Type",
      description: "The chosen Period Type has to be set for the Product on Sperse side",
      required: !0,
      defaultValue: "Monthly",
      options: {
        disabled: !1,
        options: [
          {
            label: "Monthly",
            value: "Monthly"
          },
          {
            label: "Annual",
            value: "Annual"
          },
          {
            label: "LifeTime",
            value: "LifeTime"
          }
        ]
      }
    }),
    hasRecurringBilling: import_pieces_framework4.Property.StaticDropdown({
      displayName: "Is it Recurring Billing",
      required: !1,
      defaultValue: !1,
      options: {
        disabled: !1,
        options: [
          {
            label: "Yes",
            value: !0
          },
          {
            label: "No",
            value: !1
          }
        ]
      }
    })
  },
  async run(context) {
    let subscription = {
      contactId: context.propsValue.contactId,
      contactXref: context.propsValue.contactXref,
      products: [
        {
          productId: context.propsValue.productId,
          productCode: context.propsValue.productCode,
          paymentPeriodType: context.propsValue.paymentPeriodType,
          hasRecurringBilling: context.propsValue.hasRecurringBilling
        }
      ],
      productId: context.propsValue.productId,
      productCode: context.propsValue.productCode,
      paymentPeriodType: context.propsValue.paymentPeriodType,
      hasRecurringBilling: context.propsValue.hasRecurringBilling
    }, res = await import_pieces_common3.httpClient.sendRequest({
      method: import_pieces_common3.HttpMethod.PUT,
      url: `${context.auth.props.base_url}/api/services/CRM/OrderSubscription/Update`,
      headers: {
        "api-key": context.auth.props.api_key,
        // Pass API key in headers
        "Content-Type": "application/json"
      },
      body: __spreadValues({}, subscription)
    });
    return {
      status: res.status,
      body: res.body
    };
  }
});

// ../ap/packages/pieces/community/upgradechat/src/lib/actions/create-invoice.ts
var import_pieces_framework5 = require("@activepieces/pieces-framework");
var import_pieces_common4 = require("@activepieces/pieces-common");
var getCurrentDateInISOFormat = () => (/* @__PURE__ */ new Date()).toISOString(), createInvoice = (0, import_pieces_framework5.createAction)({
  name: "createInvoice",
  displayName: "Create Invoice",
  description: "Creates a new invoice in the CRM.",
  audience: "both",
  aiMetadata: {
    description: "Creates an invoice in the Sperse/Upgrade.chat CRM for a contact, including a single line item, billing/shipping addresses, totals, and an associated payment transaction. Choose this to bill a contact or record a payment against them. Requires the contact (Contact ID or External Contact ID), a status, invoice number, dates, currency, description, quantity, and the transaction date. Not idempotent: each call records a new invoice and transaction, so repeating it produces duplicates.",
    idempotent: !1
  },
  auth: upgradechatAuth,
  props: {
    contactId: import_pieces_framework5.Property.Number({
      displayName: "Contact ID",
      description: "Sperse Contact ID. Will be used for looking a client",
      defaultValue: 0,
      required: !1
    }),
    contactXref: import_pieces_framework5.Property.ShortText({
      displayName: "External Contact ID",
      description: "External Contact Reference (ID) . Will be used for looking a client",
      required: !1
    }),
    status: import_pieces_framework5.Property.StaticDropdown({
      displayName: "Status",
      required: !0,
      defaultValue: "Draft",
      options: {
        disabled: !1,
        options: [
          {
            label: "Draft",
            value: "Draft"
          },
          {
            label: "Final",
            value: "Final"
          },
          {
            label: "Paid",
            value: "Paid"
          },
          {
            label: "Sent",
            value: "Sent"
          }
        ]
      }
    }),
    invoiceNo: import_pieces_framework5.Property.Number({
      displayName: "Invoice No.",
      defaultValue: 0,
      required: !0
    }),
    date: import_pieces_framework5.Property.DateTime({
      displayName: "Date of the Invoice",
      description: "should be like this: 2024-06-11T11:11:41Z",
      defaultValue: getCurrentDateInISOFormat(),
      required: !0
    }),
    dueDate: import_pieces_framework5.Property.DateTime({
      displayName: "Due Date of the Invoice",
      description: "should be like this: 2024-06-11T11:11:41Z",
      required: !0
    }),
    currencyId: import_pieces_framework5.Property.StaticDropdown({
      displayName: "Currency Id",
      required: !0,
      defaultValue: "USD",
      options: {
        disabled: !1,
        options: [
          {
            label: "USD",
            value: "USD"
          },
          {
            label: "JPY",
            value: "JPY"
          },
          {
            label: "IND",
            value: "IND"
          },
          {
            label: "EUR",
            value: "EUR"
          },
          {
            label: "GBP",
            value: "GBP"
          },
          {
            label: "AUD",
            value: "AUD"
          },
          {
            label: "CAD",
            value: "CAD"
          },
          {
            label: "CHF",
            value: "CHF"
          },
          {
            label: "CNY",
            value: "CNY"
          },
          {
            label: "SEK",
            value: "SEK"
          },
          {
            label: "NZD",
            value: "NZD"
          }
        ]
      }
    }),
    grandTotal: import_pieces_framework5.Property.Number({
      displayName: "Grand Total",
      defaultValue: 0,
      required: !1
    }),
    discountTotal: import_pieces_framework5.Property.Number({
      displayName: "Discount Total",
      defaultValue: 0,
      required: !1
    }),
    shippingTotal: import_pieces_framework5.Property.Number({
      displayName: "Shipping Total",
      defaultValue: 0,
      required: !1
    }),
    taxTotal: import_pieces_framework5.Property.Number({
      displayName: "Tax Total",
      defaultValue: 0,
      required: !1
    }),
    // billing
    bCompany: import_pieces_framework5.Property.ShortText({
      displayName: "Billing Company",
      required: !1
    }),
    bFirstName: import_pieces_framework5.Property.ShortText({
      displayName: "First Name",
      required: !1
    }),
    bLastName: import_pieces_framework5.Property.ShortText({
      displayName: "Last Name",
      required: !1
    }),
    bPhone: import_pieces_framework5.Property.ShortText({
      displayName: "Phone",
      required: !1
    }),
    bEmail: import_pieces_framework5.Property.ShortText({
      displayName: "Email",
      required: !1
    }),
    bCountryId: import_pieces_framework5.Property.ShortText({
      displayName: "Country Id",
      required: !1
    }),
    bStateId: import_pieces_framework5.Property.ShortText({
      displayName: "State Id",
      required: !1
    }),
    bStateName: import_pieces_framework5.Property.ShortText({
      displayName: "State Name",
      required: !1
    }),
    bCity: import_pieces_framework5.Property.ShortText({
      displayName: "City",
      required: !1
    }),
    bZip: import_pieces_framework5.Property.ShortText({
      displayName: "Zip",
      required: !1
    }),
    bAddress1: import_pieces_framework5.Property.LongText({
      displayName: "Billing Address 1",
      required: !1
    }),
    bAddress2: import_pieces_framework5.Property.LongText({
      displayName: "Billing Address 2",
      required: !1
    }),
    // shipping
    sCompany: import_pieces_framework5.Property.ShortText({
      displayName: "Shipping Company",
      required: !1
    }),
    sFirstName: import_pieces_framework5.Property.ShortText({
      displayName: "First Name",
      required: !1
    }),
    sLastName: import_pieces_framework5.Property.ShortText({
      displayName: "Last Name",
      required: !1
    }),
    sPhone: import_pieces_framework5.Property.ShortText({
      displayName: "Phone",
      required: !1
    }),
    sEmail: import_pieces_framework5.Property.ShortText({
      displayName: "Email",
      required: !1
    }),
    sCountryId: import_pieces_framework5.Property.ShortText({
      displayName: "Country Id",
      required: !1
    }),
    sStateId: import_pieces_framework5.Property.ShortText({
      displayName: "State Id",
      required: !1
    }),
    sStateName: import_pieces_framework5.Property.ShortText({
      displayName: "State Name",
      required: !1
    }),
    sCity: import_pieces_framework5.Property.ShortText({
      displayName: "City",
      required: !1
    }),
    sZip: import_pieces_framework5.Property.ShortText({
      displayName: "Zip",
      required: !1
    }),
    sAddress1: import_pieces_framework5.Property.LongText({
      displayName: "Shipping Address 1",
      required: !1
    }),
    sAddress2: import_pieces_framework5.Property.LongText({
      displayName: "Shipping Address 2",
      required: !1
    }),
    //
    note: import_pieces_framework5.Property.LongText({
      displayName: "Invoice Note",
      required: !1
    }),
    invoiceDescription: import_pieces_framework5.Property.LongText({
      displayName: "Invoice Description",
      required: !0
    }),
    // line
    quantity: import_pieces_framework5.Property.Number({
      displayName: "Quantity",
      defaultValue: 0,
      required: !0
    }),
    rate: import_pieces_framework5.Property.Number({
      displayName: "Rate",
      defaultValue: 0,
      required: !1
    }),
    itemTotal: import_pieces_framework5.Property.Number({
      displayName: "Total Item Price",
      defaultValue: 0,
      required: !1
    }),
    commissionableAmount: import_pieces_framework5.Property.Number({
      displayName: "Commissionable Amount",
      defaultValue: 0,
      required: !1
    }),
    unitId: import_pieces_framework5.Property.StaticDropdown({
      displayName: "Unit Id",
      required: !1,
      defaultValue: "Unit",
      options: {
        disabled: !1,
        options: [
          {
            label: "Unit",
            value: "Unit"
          },
          {
            label: "Day",
            value: "Day"
          },
          {
            label: "Month",
            value: "Month"
          },
          {
            label: "Year",
            value: "Year"
          },
          {
            label: "Hour",
            value: "Hour"
          },
          {
            label: "Kilogram",
            value: "Kilogram"
          },
          {
            label: "Zone",
            value: "Zone"
          },
          {
            label: "Package",
            value: "Package"
          },
          {
            label: "Pound",
            value: "Pound"
          },
          {
            label: "Piece",
            value: "Piece"
          },
          {
            label: "Feet",
            value: "Feet"
          },
          {
            label: "Custom",
            value: "Custom"
          }
        ]
      }
    }),
    productCode: import_pieces_framework5.Property.ShortText({
      displayName: "Product Code",
      description: "Product Code. We will look up the product",
      required: !1
    }),
    itemDescription: import_pieces_framework5.Property.ShortText({
      displayName: "Description",
      required: !1
    }),
    sortOrder: import_pieces_framework5.Property.Number({
      displayName: "Sort Order",
      defaultValue: 0,
      required: !1
    }),
    // transactions
    transactionDate: import_pieces_framework5.Property.DateTime({
      displayName: "Transaction Date",
      description: "should be like this: 2024-06-11T11:11:41Z",
      defaultValue: getCurrentDateInISOFormat(),
      required: !0
    }),
    transactionDescription: import_pieces_framework5.Property.ShortText({
      displayName: "Transaction Description",
      required: !1
    }),
    amount: import_pieces_framework5.Property.Number({
      displayName: "Amount",
      defaultValue: 0,
      required: !1
    }),
    gatewayName: import_pieces_framework5.Property.ShortText({
      displayName: "Gateway Name",
      required: !1
    }),
    gatewayTransactionId: import_pieces_framework5.Property.ShortText({
      displayName: "Gateway Transaction Id",
      required: !1
    }),
    historicalData: import_pieces_framework5.Property.StaticDropdown({
      displayName: "Historical Data",
      description: "Pass true if this is not actual transaction. Should be False by default",
      required: !0,
      defaultValue: !1,
      options: {
        disabled: !1,
        options: [
          {
            label: "true",
            value: !0
          },
          {
            label: "false",
            value: !1
          }
        ]
      }
    })
  },
  async run(context) {
    let invoice = {
      contactId: context.propsValue.contactId,
      contactXref: context.propsValue.contactXref,
      status: context.propsValue.status,
      number: context.propsValue.invoiceNo,
      date: context.propsValue.date,
      dueDate: context.propsValue.dueDate,
      currencyId: context.propsValue.currencyId,
      grandTotal: context.propsValue.grandTotal,
      discountTotal: context.propsValue.discountTotal,
      shippingTotal: context.propsValue.shippingTotal,
      taxTotal: context.propsValue.taxTotal,
      billingAddress: {
        countryId: context.propsValue.bCountryId,
        stateId: context.propsValue.bStateId,
        stateName: context.propsValue.bStateName,
        city: context.propsValue.bCity,
        zip: context.propsValue.bZip,
        address1: context.propsValue.bAddress1,
        address2: context.propsValue.bAddress2,
        firstName: context.propsValue.bFirstName,
        lastName: context.propsValue.bLastName,
        company: context.propsValue.bCompany,
        email: context.propsValue.bEmail,
        phone: context.propsValue.bPhone
      },
      shippingAddress: {
        countryId: context.propsValue.sCountryId,
        stateId: context.propsValue.sStateId,
        stateName: context.propsValue.sStateName,
        city: context.propsValue.sCity,
        zip: context.propsValue.sZip,
        address1: context.propsValue.sAddress1,
        address2: context.propsValue.sAddress2,
        firstName: context.propsValue.sFirstName,
        lastName: context.propsValue.sLastName,
        company: context.propsValue.sCompany,
        email: context.propsValue.sEmail,
        phone: context.propsValue.sPhone
      },
      description: context.propsValue.invoiceDescription,
      note: context.propsValue.note,
      lines: [
        {
          quantity: context.propsValue.quantity,
          rate: context.propsValue.rate,
          total: context.propsValue.itemTotal,
          commissionableAmount: context.propsValue.commissionableAmount,
          unitId: context.propsValue.unitId,
          productCode: context.propsValue.productCode,
          description: context.propsValue.itemDescription,
          sortOrder: context.propsValue.sortOrder
        }
      ],
      transactions: [
        {
          date: context.propsValue.transactionDate,
          description: context.propsValue.transactionDescription,
          amount: context.propsValue.amount,
          gatewayName: context.propsValue.gatewayName,
          gatewayTransactionId: context.propsValue.gatewayTransactionId
        }
      ],
      historicalData: context.propsValue.historicalData
    }, res = await import_pieces_common4.httpClient.sendRequest({
      method: import_pieces_common4.HttpMethod.POST,
      url: `${context.auth.props.base_url}/api/services/CRM/Import/ImportInvoice`,
      headers: {
        "api-key": context.auth.props.api_key,
        // Pass API key in headers
        "Content-Type": "application/json"
      },
      body: __spreadValues({}, invoice)
    });
    return {
      status: res.status,
      body: res.body
    };
  }
});

// ../ap/packages/pieces/community/upgradechat/src/lib/actions/create-product.ts
var import_pieces_framework6 = require("@activepieces/pieces-framework");
var import_pieces_common5 = require("@activepieces/pieces-common");
var createProduct = (0, import_pieces_framework6.createAction)({
  name: "createProduct",
  displayName: "Create Product",
  description: "Creates a new product in the CRM",
  audience: "both",
  aiMetadata: {
    description: "Imports a product into the Sperse/Upgrade.chat CRM catalog (General, Event, Subscription, or Digital type) with pricing, currency, unit, payment cycle, and subscription-fee options. Choose this to set up a sellable product before invoicing or subscribing contacts to it. Requires Product Type, Product Name, and SKU (the unique product code); price, unit, and cycle fields are required only for certain product types. The import is keyed on the SKU, so re-running with the same SKU updates that product rather than creating a duplicate.",
    idempotent: !0
  },
  auth: upgradechatAuth,
  props: {
    productType: import_pieces_framework6.Property.StaticDropdown({
      displayName: "Product Type",
      required: !0,
      defaultValue: "General",
      options: {
        disabled: !1,
        options: [
          {
            label: "General",
            value: "General"
          },
          {
            label: "Event",
            value: "Event"
          },
          {
            label: "Subscription",
            value: "Subscription"
          },
          {
            label: "Digital",
            value: "Digital"
          }
        ]
      }
    }),
    name: import_pieces_framework6.Property.ShortText({
      displayName: "Product Name",
      required: !0
    }),
    code: import_pieces_framework6.Property.ShortText({
      displayName: "SKU",
      required: !0,
      description: "Sku is the product code"
    }),
    description: import_pieces_framework6.Property.LongText({
      displayName: "Description",
      required: !1
    }),
    descriptionHTML: import_pieces_framework6.Property.LongText({
      displayName: "Description HTML",
      required: !1,
      description: "Javascript and media tags are not allowed"
    }),
    logoURL: import_pieces_framework6.Property.LongText({
      displayName: "Logo Url",
      required: !1
    }),
    groupName: import_pieces_framework6.Property.ShortText({
      displayName: "Group Name",
      required: !1
    }),
    price: import_pieces_framework6.Property.Number({
      displayName: "Price",
      required: !1,
      description: "Required for General , Digital and Event Product Type",
      defaultValue: 0
    }),
    currencyId: import_pieces_framework6.Property.StaticDropdown({
      displayName: "Currency Id",
      required: !1,
      defaultValue: "USD",
      options: {
        disabled: !1,
        options: [
          {
            label: "USD",
            value: "USD"
          },
          {
            label: "JPY",
            value: "JPY"
          },
          {
            label: "IND",
            value: "IND"
          },
          {
            label: "EUR",
            value: "EUR"
          },
          {
            label: "GBP",
            value: "GBP"
          },
          {
            label: "AUD",
            value: "AUD"
          },
          {
            label: "CAD",
            value: "CAD"
          },
          {
            label: "CHF",
            value: "CHF"
          },
          {
            label: "CNY",
            value: "CNY"
          },
          {
            label: "SEK",
            value: "SEK"
          },
          {
            label: "NZD",
            value: "NZD"
          }
        ]
      }
    }),
    unit: import_pieces_framework6.Property.StaticDropdown({
      displayName: "Unit",
      required: !1,
      description: "Required for General and Digital Product Type",
      defaultValue: "Day",
      options: {
        disabled: !1,
        options: [
          {
            label: "Day",
            value: "Day"
          },
          {
            label: "Feet",
            value: "Feet"
          },
          {
            label: "Hour",
            value: "Hour"
          },
          {
            label: "Kilogram",
            value: "Kilogram"
          },
          {
            label: "Pound",
            value: "Pound"
          },
          {
            label: "Month",
            value: "Month"
          },
          {
            label: "Package",
            value: "Package"
          },
          {
            label: "Piece",
            value: "Piece"
          },
          {
            label: "Unit",
            value: "Unit"
          },
          {
            label: "Year",
            value: "Year"
          },
          {
            label: "Zone",
            value: "Zone"
          },
          {
            label: "Custom",
            value: "Custom"
          }
        ]
      }
    }),
    frequency: import_pieces_framework6.Property.StaticDropdown({
      displayName: "Payment Cycle",
      required: !1,
      description: "Required for General and Digital Product Type",
      defaultValue: "Monthly",
      options: {
        disabled: !1,
        options: [
          {
            label: "Monthly",
            value: "Monthly"
          },
          {
            label: "Annual",
            value: "Annual"
          },
          {
            label: "LifeTime",
            value: "LifeTime"
          },
          {
            label: "OneTime",
            value: "OneTime"
          },
          {
            label: "Custom",
            value: "Custom"
          }
        ]
      }
    }),
    fee: import_pieces_framework6.Property.Number({
      displayName: "Subscription fee",
      required: !1,
      defaultValue: 0
    }),
    cycles: import_pieces_framework6.Property.Number({
      displayName: "No of cycles",
      required: !1,
      description: "Required for all except LifeTime or OneTime plan",
      defaultValue: 0
    }),
    signupFee: import_pieces_framework6.Property.Number({
      displayName: "Signup fee",
      required: !1,
      description: "Required for all except LifeTime or OneTime plan",
      defaultValue: 0
    }),
    customPeriodType: import_pieces_framework6.Property.StaticDropdown({
      displayName: "Custom Period Type",
      required: !1,
      description: "Required for Custom or OneTime Plan",
      defaultValue: "Days",
      options: {
        disabled: !1,
        options: [
          {
            label: "Days",
            value: "Days"
          },
          {
            label: "Weeks",
            value: "Weeks"
          },
          {
            label: "Months",
            value: "Months"
          },
          {
            label: "Years",
            value: "Years"
          }
        ]
      }
    }),
    customPeriodCount: import_pieces_framework6.Property.Number({
      displayName: "Custom No of Period",
      required: !1,
      description: "Required for Custom or OneTime Plan",
      defaultValue: 0
    }),
    trialDayCount: import_pieces_framework6.Property.Number({
      displayName: "Custom No of Period",
      required: !1,
      description: "Required for OneTime plan",
      defaultValue: 0
    }),
    gracePeriodDayCount: import_pieces_framework6.Property.Number({
      displayName: "Grace Period Count",
      required: !1,
      defaultValue: 0
    })
  },
  async run(context) {
    let product = {
      code: context.propsValue.code,
      name: context.propsValue.name,
      logoUrl: context.propsValue.logoURL,
      description: context.propsValue.description,
      descriptionHtml: context.propsValue.descriptionHTML,
      groupName: context.propsValue.groupName,
      type: context.propsValue.productType,
      price: context.propsValue.price,
      currencyId: context.propsValue.currencyId,
      unit: context.propsValue.unit,
      productSubscriptionOptions: [
        {
          frequency: context.propsValue.frequency,
          signupFee: context.propsValue.signupFee,
          fee: context.propsValue.fee,
          trialDayCount: context.propsValue.trialDayCount,
          customPeriodCount: context.propsValue.customPeriodCount,
          customPeriodType: context.propsValue.customPeriodType,
          cycles: context.propsValue.cycles,
          gracePeriodDayCount: context.propsValue.gracePeriodDayCount
        }
      ]
    }, res = await import_pieces_common5.httpClient.sendRequest({
      method: import_pieces_common5.HttpMethod.POST,
      url: `${context.auth.props.base_url}/api/services/CRM/Import/ImportProduct`,
      headers: {
        "api-key": context.auth.props.api_key,
        // Pass API key in headers
        "Content-Type": "application/json"
      },
      body: __spreadValues({}, product)
    });
    return {
      status: res.status,
      body: res.body
    };
  }
});

// ../ap/packages/pieces/community/upgradechat/src/lib/actions/get-contact-details.ts
var import_pieces_framework7 = require("@activepieces/pieces-framework");
var import_pieces_common6 = require("@activepieces/pieces-common"), getContactDetails = (0, import_pieces_framework7.createAction)({
  name: "getContactDetails",
  displayName: "Get Contact Details",
  description: "Get Contact Details",
  audience: "both",
  aiMetadata: {
    description: "Looks up and returns the full detail record for a single contact in the Sperse/Upgrade.chat CRM. Provide at least one identifier to resolve the contact: Contact ID, Contact Xref, Affiliate Code, User ID, or User Email (all optional individually, but a lookup needs one). Choose this to read a contact before deciding to update, invoice, or subscribe them. Read-only and idempotent.",
    idempotent: !0
  },
  auth: upgradechatAuth,
  props: {
    contactId: import_pieces_framework7.Property.Number({
      displayName: "Contact Id",
      required: !1
    }),
    xref: import_pieces_framework7.Property.ShortText({
      displayName: "Contact Xref",
      required: !1
    }),
    affiliateCode: import_pieces_framework7.Property.ShortText({
      displayName: "Affiliate Code",
      required: !1
    }),
    userId: import_pieces_framework7.Property.Number({
      displayName: "User Id",
      required: !1,
      description: "Id of the logged in user (not contact id)"
    }),
    userEmail: import_pieces_framework7.Property.LongText({
      displayName: "User Email",
      required: !1,
      description: "Email of the logged in user"
    })
  },
  async run(context) {
    let contact = {
      contactId: context.propsValue.contactId,
      xref: context.propsValue.xref,
      affiliateCode: context.propsValue.affiliateCode,
      userId: context.propsValue.userId,
      userEmail: context.propsValue.userEmail
    }, filteredContact = Object.fromEntries(
      Object.entries(contact).filter(([, value]) => value !== void 0)
    ), queryParams = new URLSearchParams(
      Object.entries(filteredContact).map(([key, value]) => [
        key,
        String(value)
      ])
    ), res = await import_pieces_common6.httpClient.sendRequest({
      method: import_pieces_common6.HttpMethod.GET,
      url: `${context.auth.props.base_url}/api/services/CRM/Contact/GetContactData?${queryParams.toString()}`,
      headers: {
        "api-key": context.auth.props.api_key,
        "Content-Type": "application/json"
      }
    });
    return {
      status: res.status,
      body: res.body
    };
  }
});

// ../ap/packages/pieces/community/upgradechat/src/lib/triggers/new-lead.ts
var import_pieces_framework8 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/upgradechat/src/lib/common/common.ts
var import_pieces_common7 = require("@activepieces/pieces-common"), upgradechatCommon = {
  subscribeWebhook: async (eventName, baseUrl, apiKey, webhookUrl) => {
    let request = {
      method: import_pieces_common7.HttpMethod.POST,
      url: `${baseUrl}/api/services/Platform/Event/Subscribe`,
      headers: {
        "api-key": apiKey,
        "Content-Type": "application/json"
      },
      body: {
        eventName,
        targetUrl: webhookUrl
      }
    }, res = await import_pieces_common7.httpClient.sendRequest(request), { id: webhookId } = res.body.result;
    return webhookId;
  },
  unsubscribeWebhook: async (baseUrl, apiKey, webhookId) => {
    let request = {
      method: import_pieces_common7.HttpMethod.POST,
      url: `${baseUrl}/api/services/Platform/Event/Unsubscribe?id=${webhookId}`,
      headers: {
        "api-key": apiKey,
        "Content-Type": "application/json"
      }
    };
    return await import_pieces_common7.httpClient.sendRequest(request);
  }
};

// ../ap/packages/pieces/community/upgradechat/src/lib/triggers/new-lead.ts
var newLead = (0, import_pieces_framework8.createTrigger)({
  auth: upgradechatAuth,
  name: "newLead",
  displayName: "New Lead",
  description: "Triggers when a new lead is created",
  aiMetadata: {
    description: "Fires when a new lead contact is created in the Sperse/Upgrade.chat CRM, delivering the full lead record (personal, business, tracking, application, and classification details). Use it to react to inbound prospects as they enter the CRM."
  },
  props: {},
  type: import_pieces_framework8.TriggerStrategy.WEBHOOK,
  sampleData: {
    id: 100500,
    group: "P",
    personalInfo: {
      fullName: {
        namePrefix: "Mr",
        firstName: "John",
        middleName: "G",
        lastName: "Smith",
        nameSuffix: "Jr",
        nickName: "Johnny"
      },
      doB: "1992-06-30T00:00:00Z",
      mobilePhone: "+12057899877",
      mobilePhoneExt: "123",
      homePhone: "+12057985632",
      homePhoneExt: "123",
      phone1: null,
      phoneExt1: null,
      phone2: null,
      phoneExt2: null,
      preferredToD: "Anytime",
      timeZone: "Pacific Standard Time",
      ssn: "123456456",
      bankCode: "BANK",
      email1: "personalemail1@gmal.com",
      email2: "personalemail2@hotmail.com",
      email3: "personalemail3@yahoo.com",
      email4: null,
      email5: null,
      drivingLicense: "ASDF4566G",
      drivingLicenseState: "NY",
      isActiveMilitaryDuty: !0,
      gender: "Male",
      fullAddress: {
        street: "999-901 Emancipation Ave",
        addressLine2: null,
        neighborhood: null,
        city: "Houston",
        stateName: "Texas",
        stateId: "TX",
        zip: "77003",
        countryName: "United States of America",
        countryId: "US",
        startDate: "2018-04-30T00:00:00Z",
        isOwner: !0
      },
      fullAddress2: null,
      fullAddress3: null,
      isUSCitizen: !0,
      webSiteUrl: "www.myprofile.com",
      facebookUrl: "www.fb.com/j.smith",
      linkedInUrl: "https://www.linkedin.com/j.smith",
      instagramUrl: "https://www.instagram.com/j.smith",
      twitterUrl: "https://twitter.com/j.smith",
      googlePlusUrl: "https://googleplus.com/j.smith",
      angelListUrl: "https://angel.co/",
      zoomUrl: "https://zoom.com",
      otherLinkUrl: "https://otherlink.com",
      photoUrl: "https://www.myprofile.com/profile-pictures/myphoto.png",
      experience: "Improvements made to two existing products, designed five completely new products for four customers.",
      profileSummary: "Highly skilled and results-oriented professional with solid academic preparation holding a Juris Doctor degree and extensive experience in intelligence and special operations seeks position in risk management.",
      interests: ["World Economy", "Baseball"],
      affiliateCode: "PA001",
      isActive: null,
      customFields: {
        customField1: null,
        customField2: null,
        customField3: null,
        customField4: null,
        customField5: null
      }
    },
    businessInfo: {
      companyName: "MyCompany LLC",
      organizationType: "Inc",
      jobTitle: "Chief Executive Officer",
      isEmployed: !0,
      employmentStartDate: "2021-12-30T00:00:00Z",
      employeeCount: 500,
      dateFounded: "2021-06-30T00:00:00Z",
      ein: "35-8896524",
      annualRevenue: 6e6,
      industry: "Sport and Entertainment",
      companyPhone: "+12057784563",
      companyPhoneExt: "123",
      companyFaxNumber: "+12057324598",
      companyEmail: "companyemail1@company.com",
      companyFullAddress: {
        street: "1500 Canton st",
        addressLine2: null,
        neighborhood: null,
        city: "Dallas",
        stateName: "Texas",
        stateId: "TX",
        zip: "75201",
        countryName: "United States of America",
        countryId: "US",
        startDate: "2018-04-30T00:00:00Z",
        isOwner: !1
      },
      companyWebSiteUrl: "www.mycompany.com",
      companyFacebookUrl: "www.fb.com/mycompany",
      companyLinkedInUrl: "https://www.linkedin.com/mycompany",
      companyInstagramUrl: "https://www.instagram.com/mycompany",
      companyTwitterUrl: "https://twitter.com/mycompany",
      companyGooglePlusUrl: "https://googleplus.com/mycompany",
      companyCrunchbaseUrl: "https://www.crunchbase.com/mycompany",
      companyBBBUrl: "https://www.bbb.org/en/us/overview-of-bbb-ratings",
      companyPinterestUrl: "https://www.pinterest.com/mycompany",
      companyDomainUrl: "https://www.domain.com/mycompany",
      companyAlexaUrl: "https://www.alexa.com/mycompany",
      companyOpenCorporatesUrl: "https://www.opencorporates.com/mycompany",
      companyGlassDoorUrl: "https://www.classdoor.com/mycompany",
      companyTrustpilotUrl: "https://www.trustpilot.com/mycompany",
      companyFollowersUrl: "https://www.followers.com/mycompany",
      companyYoutubeUrl: "https://www.youtube.com/mycompany",
      companyYelpUrl: "https://www.yelp.com/mycompany",
      companyRSSUrl: "https://www.rss.com/mycompany",
      companyNavUrl: "https://www.nav.com/mycompany",
      companyAngelListUrl: "https://www.angelist.com/mycompany",
      companyCalendlyUrl: "https://www.calendly.com/mycompany",
      companyZoomUrl: "https://zoom.com/mycompany",
      companyOtherLinkUrl: "https://www.otherlink.com/mycompany",
      companyLogoUrl: "https://www.mycompany.com/images/companylogo/logo.png",
      workPhone1: "+12057412354",
      workPhone1Ext: "123",
      workPhone2: "+12057741236",
      workPhone2Ext: "123",
      workEmail1: "workemail1@company.com",
      workEmail2: "workemail2@company.com",
      workEmail3: "workemail3@company.com",
      workFullAddress: {
        street: "1502-1702 Strawberry Rd",
        addressLine2: null,
        neighborhood: null,
        city: "Pasadena",
        stateName: "Texas",
        stateId: "TX",
        zip: "77502",
        countryName: "United States of America",
        countryId: "US",
        startDate: "2020-05-30T00:00:00Z",
        isOwner: !1
      },
      affiliateCode: "CA0001"
    },
    dateCreated: "2022-06-23T13:32:54.9451405Z",
    ipAddress: "192.168.0.1",
    trackingInfo: {
      sourceCode: "Code",
      channelCode: "ChannelCode",
      affiliateCode: "414CODE",
      refererUrl: "http://www.refererurl.com/referpage.html",
      entryUrl: "https://entryurl.com/start-now/?ref=wow",
      userAgent: "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/79.0.3945.88 Safari/537.36",
      clientIp: "192.168.0.1"
    },
    applicationInfo: {
      applicationId: "771XYZ23234G",
      applicantId: "771XYZSD",
      applicantUserId: 10229,
      clickId: "2B69283E-9433-4CE6-A24E-D8809C050B70",
      requestedLoanAmount: 89,
      incomeType: "Benefits",
      netMonthlyIncome: 899,
      payFrequency: "Monthly",
      payNextDate: "2022-07-30T13:32:54.945358Z",
      bankName: "Bank Name",
      bankAccountType: "Checking",
      monthsAtBank: 24,
      bankAccountNumber: "26005325777412",
      creditScoreRating: "Excellent",
      loanReason: "AutoPurchase",
      creditCardDebtAmount: 56898
    },
    classificationInfo: {
      rating: "10",
      lists: ["My List 1", "My List 2", "My List 3"],
      tags: ["My Tag 1", "My Tag 2", "My Tag 3"],
      partnerTypeName: "My Partner Type"
    },
    eventTime: "2022-06-30T13:32:54Z"
  },
  async onEnable(context) {
    var _a;
    let webhookId = await upgradechatCommon.subscribeWebhook(
      "LeadCreated",
      context.auth.props.base_url,
      context.auth.props.api_key,
      context.webhookUrl
    );
    await ((_a = context.store) == null ? void 0 : _a.put("_new_lead_trigger", {
      webhookId
    }));
  },
  async onDisable(context) {
    var _a;
    let response = await ((_a = context.store) == null ? void 0 : _a.get(
      "_new_lead_trigger"
    ));
    response != null && await upgradechatCommon.unsubscribeWebhook(
      context.auth.props.base_url,
      context.auth.props.api_key,
      response.webhookId
    );
  },
  async run(context) {
    return [context.payload.body];
  }
});

// ../ap/packages/pieces/community/upgradechat/src/lib/triggers/new-payment.ts
var import_pieces_framework9 = require("@activepieces/pieces-framework");
var newPayment = (0, import_pieces_framework9.createTrigger)({
  auth: upgradechatAuth,
  name: "newPayment",
  displayName: "New Payment",
  description: "Triggers when a new payment is created",
  aiMetadata: {
    description: "Fires when a payment is recorded in the Sperse/Upgrade.chat CRM (Payment.Created event), delivering the related contact, invoice, and transaction details. Use it to react to incoming payments, such as fulfilling orders or sending receipts."
  },
  props: {},
  type: import_pieces_framework9.TriggerStrategy.WEBHOOK,
  sampleData: {
    contact: {
      email: "s@gmail.com",
      fullName: "Test User",
      id: 636,
      phone: "98877676565"
    },
    invoice: {
      currencyId: "USD",
      date: "2024-06-10T00:00:00Z",
      description: "just a description",
      discountTotal: 0,
      grandTotal: 100,
      id: 457,
      number: "INV - 20240610 - 746C",
      shippingTotal: 0,
      taxTotal: 0,
      lines: [
        {
          description: "Me Spacial",
          productId: 810,
          quantity: 1,
          rate: 100,
          total: 100,
          unitId: "Month"
        }
      ]
    },
    transaction: {
      amount: 100,
      currencyId: "USD",
      date: "2024-06-10T09:36:01.832Z",
      gatewayName: null,
      gatewayTransactionId: null,
      id: 403,
      isSuccessful: !0,
      type: "Sale"
    },
    eventTime: "2024-06-10T09:36:08",
    eventType: "Payment.Created"
  },
  async onEnable(context) {
    var _a;
    let webhookId = await upgradechatCommon.subscribeWebhook(
      "Payment.Created",
      context.auth.props.base_url,
      context.auth.props.api_key,
      context.webhookUrl
    );
    await ((_a = context.store) == null ? void 0 : _a.put("_new_payment_trigger", {
      webhookId
    }));
  },
  async onDisable(context) {
    var _a;
    let response = await ((_a = context.store) == null ? void 0 : _a.get(
      "_new_payment_trigger"
    ));
    response != null && await upgradechatCommon.unsubscribeWebhook(
      context.auth.props.base_url,
      context.auth.props.api_key,
      response.webhookId
    );
  },
  async run(context) {
    return [context.payload.body];
  }
});

// ../ap/packages/pieces/community/upgradechat/src/lib/triggers/new-subscription.ts
var import_pieces_framework10 = require("@activepieces/pieces-framework");
var newSubscription = (0, import_pieces_framework10.createTrigger)({
  auth: upgradechatAuth,
  name: "newSubscription",
  displayName: "New Subscription",
  description: "triggers when a new subscription is created",
  aiMetadata: {
    description: "Fires when a subscription is created or updated in the Sperse/Upgrade.chat CRM (Subscription.CreatedOrUpdated event), delivering the contact, subscription name, dates, amount, frequency, and status. Use it to react to new or changed memberships, including renewals, plan changes, and cancellations."
  },
  props: {},
  type: import_pieces_framework10.TriggerStrategy.WEBHOOK,
  sampleData: {
    eventType: "Subscription.CreatedOrUpdated",
    contactId: 3994,
    fullname: "Frank Micheal",
    email: "tray@sperse.com",
    id: "536778",
    name: "Subscription_Name",
    startDate: "2024-06-30T09:29:43.6271352Z",
    endDate: "2025-06-30T09:29:43.6271352Z",
    amount: 100,
    frequency: "Annual",
    trialDayCount: "4",
    gracePeriodCount: "10",
    statusId: "A",
    cancelationReason: "",
    eventTime: "2024-09-06T00:29:07"
  },
  async onEnable(context) {
    var _a;
    let webhookId = await upgradechatCommon.subscribeWebhook(
      "Subscription.CreatedOrUpdated",
      context.auth.props.base_url,
      context.auth.props.api_key,
      context.webhookUrl
    );
    await ((_a = context.store) == null ? void 0 : _a.put("_new_subscription_trigger", {
      webhookId
    }));
  },
  async onDisable(context) {
    var _a;
    let response = await ((_a = context.store) == null ? void 0 : _a.get(
      "_new_subscription_trigger"
    ));
    response != null && await upgradechatCommon.unsubscribeWebhook(
      context.auth.props.base_url,
      context.auth.props.api_key,
      response.webhookId
    );
  },
  async run(context) {
    return [context.payload.body];
  }
});

// ../ap/packages/pieces/community/upgradechat/src/index.ts
var import_pieces_framework12 = require("@activepieces/pieces-framework");
var upgradechat = (0, import_pieces_framework11.createPiece)({
  displayName: "Upgrade.chat",
  description: "Supercharge your Discord or Telegram communities with subscription payments and membership tools.",
  auth: upgradechatAuth,
  minimumSupportedRelease: "0.30.0",
  logoUrl: "https://cdn.activepieces.com/pieces/upgradechat.png",
  authors: ["Trayshmhirk", "OmarSayed"],
  categories: [import_pieces_framework12.PieceCategory.SALES_AND_CRM],
  actions: [
    addOrUpdateContact,
    addOrUpdateContactExtended,
    addOrUpdateSubscription,
    createInvoice,
    createProduct,
    getContactDetails
  ],
  triggers: [newLead, newPayment, newSubscription]
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  upgradechat
});
