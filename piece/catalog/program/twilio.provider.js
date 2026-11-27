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

// ../ap/packages/pieces/community/twilio/src/index.ts
var index_exports = {};
__export(index_exports, {
  twilio: () => twilio,
  twilioAuth: () => twilioAuth
});
module.exports = __toCommonJS(index_exports);
var import_pieces_common12 = require("@activepieces/pieces-common"), import_pieces_framework12 = require("@activepieces/pieces-framework"), import_pieces_framework13 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/twilio/src/lib/action/send-sms.ts
var import_pieces_framework2 = require("@activepieces/pieces-framework"), import_pieces_common2 = require("@activepieces/pieces-common");

// ../ap/packages/pieces/community/twilio/src/lib/common/index.ts
var import_pieces_framework = require("@activepieces/pieces-framework"), import_pieces_common = require("@activepieces/pieces-common");
var twilioCommon = {
  phone_number: import_pieces_framework.Property.Dropdown({
    auth: twilioAuth,
    description: "The phone number to send the message from",
    displayName: "From",
    required: !0,
    refreshers: [],
    options: async ({ auth }) => {
      if (!auth)
        return {
          disabled: !0,
          placeholder: "connect your account first",
          options: []
        };
      let basicAuthProperty = auth;
      return {
        disabled: !1,
        options: (await callTwilioApi(import_pieces_common.HttpMethod.GET, "IncomingPhoneNumbers.json", {
          account_sid: basicAuthProperty.username,
          auth_token: basicAuthProperty.password
        })).body.incoming_phone_numbers.map((number) => ({
          value: number.phone_number,
          label: number.friendly_name
        }))
      };
    }
  })
}, callTwilioApi = async (method, path, auth, body) => await import_pieces_common.httpClient.sendRequest({
  method,
  url: `https://api.twilio.com/2010-04-01/Accounts/${auth.account_sid}/${path}`,
  authentication: {
    type: import_pieces_common.AuthenticationType.BASIC,
    username: auth.account_sid,
    password: auth.auth_token
  },
  headers: {
    "Content-Type": "application/x-www-form-urlencoded"
  },
  body
});

// ../ap/packages/pieces/community/twilio/src/lib/action/send-sms.ts
var twilioSendSms = (0, import_pieces_framework2.createAction)({
  auth: twilioAuth,
  name: "send_sms",
  classification: "WRITE",
  description: "Send a new SMS message",
  audience: "both",
  aiMetadata: { description: "Sends an SMS text message from a Twilio phone number to a recipient. Use to notify or message a person by text. Requires the destination number, message body, and a Twilio-owned sender number; sending costs money and delivers a separate message on every call, so it is not idempotent.", idempotent: !1 },
  displayName: "Send SMS",
  props: {
    from: twilioCommon.phone_number,
    body: import_pieces_framework2.Property.ShortText({
      description: "The body of the message to send",
      displayName: "Message Body",
      required: !0
    }),
    to: import_pieces_framework2.Property.ShortText({
      description: "The phone number to send the message to",
      displayName: "To",
      required: !0
    })
  },
  async run(context) {
    let { body, to, from } = context.propsValue, account_sid = context.auth.username, auth_token = context.auth.password;
    return await callTwilioApi(
      import_pieces_common2.HttpMethod.POST,
      "Messages.json",
      { account_sid, auth_token },
      {
        From: from,
        Body: body,
        To: to
      }
    );
  }
});

// ../ap/packages/pieces/community/twilio/src/lib/trigger/new-incoming-sms.ts
var import_pieces_framework3 = require("@activepieces/pieces-framework"), import_pieces_common3 = require("@activepieces/pieces-common");
var twilioNewIncomingSms = (0, import_pieces_framework3.createTrigger)({
  auth: twilioAuth,
  name: "new_incoming_sms",
  classification: "READ",
  displayName: "New Incoming SMS",
  description: "Triggers when a new SMS message is received",
  aiMetadata: {
    description: "Fires when an inbound SMS message is received on the specified Twilio phone number. Each event represents one incoming text message with its sender, body, and metadata."
  },
  props: {
    phone_number: twilioCommon.phone_number
  },
  sampleData: {
    body: "Hello",
    num_segments: "1",
    direction: "inbound",
    from: "+12184191735",
    date_updated: "Wed, 08 Feb 2023 01:40:51 +0000",
    price: null,
    error_message: null,
    uri: "/2010-04-01/Accounts/ACc0ea1238d61fe90d78a69a3de71d45619/Messages/SM8c3920d3f2ac481ba83e639a69dadd63.json",
    account_sid: "ACc0ea716d61fe90d78a123a3de71d45619",
    num_media: "0",
    to: "+12184191735",
    date_created: "Wed, 08 Feb 2023 01:40:50 +0000",
    status: "failed",
    sid: "SM8c3920d3f2ac481ba83e639a69dadd63",
    date_sent: "Wed, 08 Feb 2023 01:40:51 +0000",
    messaging_service_sid: "MG88e323e6a88ce67ba3bf12e1bcb7e0b8",
    error_code: 21211,
    price_unit: "USD",
    api_version: "2010-04-01",
    subresource_uris: {
      media: "/2010-04-01/Accounts/AC_EXAMPLE_ACCOUNT_SID/Messages/SM8c3920d3f2ac481ba83e639a69dadd63/Media.json",
      feedback: "/2010-04-01/Accounts/AC_EXAMPLE_ACCOUNT_SID/Messages/SM8c3920d3f2ac481ba83e639a69dadd63/Feedback.json"
    }
  },
  // Twilio API only allows one webhook per phone number, so we need to poll
  type: import_pieces_framework3.TriggerStrategy.POLLING,
  async onEnable(context) {
    let { phone_number } = context.propsValue, account_sid = context.auth.username, auth_token = context.auth.password, response = await callTwilioApi(
      import_pieces_common3.HttpMethod.GET,
      `Messages.json?PageSize=20&To=${phone_number}`,
      { account_sid, auth_token },
      {}
    );
    await context.store.put("_new_incoming_sms_trigger", {
      lastMessageId: response.body.messages.length === 0 ? null : response.body.messages[0].sid
    });
  },
  async onDisable(context) {
    await context.store.put("_new_incoming_sms_trigger", null);
  },
  async run(context) {
    let account_sid = context.auth.username, auth_token = context.auth.password, newMessages = [], lastMessage = await context.store.get(
      "_new_incoming_sms_trigger"
    ), currentUri = `2010-04-01/Accounts/${account_sid}/Messages.json?PageSize=20&To=${context.propsValue.phone_number}`, firstMessageId;
    for (; currentUri != null; ) {
      let res = await import_pieces_common3.httpClient.sendRequest({
        method: import_pieces_common3.HttpMethod.GET,
        url: `https://api.twilio.com/${currentUri}`,
        authentication: {
          type: import_pieces_common3.AuthenticationType.BASIC,
          username: account_sid,
          password: auth_token
        }
      }), messages = res.body.messages;
      !firstMessageId && messages.length > 0 && (firstMessageId = messages[0].sid), currentUri = res.body.next_page_uri;
      for (let i = 0; i < messages.length; i++) {
        let message = messages[i];
        if (message.sid === (lastMessage == null ? void 0 : lastMessage.lastMessageId)) {
          currentUri = null;
          break;
        }
        message.direction === "inbound" && newMessages.push(message);
      }
    }
    return await context.store.put("_new_incoming_sms_trigger", {
      lastMessageId: firstMessageId != null ? firstMessageId : lastMessage.lastMessageId
    }), newMessages;
  }
});

// ../ap/packages/pieces/community/twilio/src/lib/action/phone-number-lookup.ts
var import_pieces_framework4 = require("@activepieces/pieces-framework"), import_pieces_common4 = require("@activepieces/pieces-common");
var twilioPhoneNumberLookup = (0, import_pieces_framework4.createAction)({
  auth: twilioAuth,
  name: "phone_number_lookup",
  classification: "READ",
  description: "Lookup information about a phone number.",
  audience: "both",
  aiMetadata: { description: "Looks up carrier and line-type intelligence for a single phone number via the Twilio Lookup API. Use to validate or enrich a number (e.g. detect mobile vs landline, carrier) before messaging or calling it. Read-only and idempotent.", idempotent: !0 },
  displayName: "Phone Number Lookup",
  props: {
    phone_number: import_pieces_framework4.Property.ShortText({
      displayName: "Phone Number",
      required: !0
    })
  },
  async run(context) {
    let { phone_number } = context.propsValue, account_sid = context.auth.username, auth_token = context.auth.password;
    return (await import_pieces_common4.httpClient.sendRequest({
      method: import_pieces_common4.HttpMethod.GET,
      url: `https://lookups.twilio.com/v2/PhoneNumbers/${encodeURIComponent(phone_number)}`,
      authentication: {
        type: import_pieces_common4.AuthenticationType.BASIC,
        username: account_sid,
        password: auth_token
      },
      queryParams: {
        Fields: "line_type_intelligence"
      }
    })).body;
  }
});

// ../ap/packages/pieces/community/twilio/src/lib/action/make-call.ts
var import_pieces_framework5 = require("@activepieces/pieces-framework"), import_pieces_common5 = require("@activepieces/pieces-common");
var twilioMakeCall = (0, import_pieces_framework5.createAction)({
  auth: twilioAuth,
  name: "make_call",
  classification: "WRITE",
  description: "Call a number and say a message.",
  audience: "both",
  aiMetadata: { description: "Places an outbound voice call from a Twilio number that reads a text-to-speech message to the recipient. Use to deliver a spoken notification or alert by phone. Requires both numbers in E.164 format and the message text; each call places a real, billable phone call, so it is not idempotent.", idempotent: !1 },
  displayName: "Call Phone",
  props: {
    from: twilioCommon.phone_number,
    to: import_pieces_framework5.Property.ShortText({
      displayName: "To",
      description: "The phone number to call. Must be in E.164 format (e.g., +15558675310).",
      required: !0
    }),
    message: import_pieces_framework5.Property.LongText({
      displayName: "Message to Say",
      description: "The text message to be converted to speech and spoken to the recipient.",
      required: !0
    }),
    voice: import_pieces_framework5.Property.StaticDropdown({
      displayName: "Voice",
      description: "The voice to use for the text-to-speech message.",
      required: !1,
      options: {
        options: [
          { label: "Alice (Default)", value: "alice" },
          { label: "Man", value: "man" },
          { label: "Woman", value: "woman" }
        ]
      }
    }),
    language: import_pieces_framework5.Property.StaticDropdown({
      displayName: "Language",
      description: "The language to use for the text-to-speech message.",
      required: !1,
      options: {
        options: [
          { label: "English (US)", value: "en-US" },
          { label: "English (UK)", value: "en-GB" },
          { label: "Spanish", value: "es-ES" },
          { label: "French", value: "fr-FR" },
          { label: "German", value: "de-DE" }
        ]
      }
    }),
    sendDigits: import_pieces_framework5.Property.ShortText({
      displayName: "Send DTMF Tones",
      description: "A string of keys to dial after the call is connected. Use 'w' for a half-second pause.",
      required: !1
    }),
    timeout: import_pieces_framework5.Property.Number({
      displayName: "Timeout (seconds)",
      description: "The number of seconds to let the phone ring before assuming no answer. Default is 60.",
      required: !1
    })
  },
  async run(context) {
    let { from, to, message, voice, language, sendDigits, timeout } = context.propsValue, account_sid = context.auth.username, auth_token = context.auth.password, voiceAttr = voice ? ` voice="${voice}"` : "", langAttr = language ? ` language="${language}"` : "", escapedMessage = message.replace(/&/g, "&amp;").replace(/</g, "&lt;").replace(/>/g, "&gt;").replace(/"/g, "&quot;").replace(/'/g, "&apos;"), twiml = `<Response><Say${voiceAttr}${langAttr}>${escapedMessage}</Say></Response>`, bodyParams = {
      From: from,
      To: to,
      Twiml: twiml
    };
    return sendDigits && (bodyParams.SendDigits = sendDigits), timeout && (bodyParams.Timeout = timeout), (await callTwilioApi(
      import_pieces_common5.HttpMethod.POST,
      "Calls.json",
      { account_sid, auth_token },
      bodyParams
    )).body;
  }
});

// ../ap/packages/pieces/community/twilio/src/lib/action/get-message.ts
var import_pieces_framework6 = require("@activepieces/pieces-framework"), import_pieces_common6 = require("@activepieces/pieces-common");
var twilioGetMessage = (0, import_pieces_framework6.createAction)({
  auth: twilioAuth,
  name: "get_message",
  classification: "READ",
  description: "Retrieves the details of a specific message.",
  audience: "both",
  aiMetadata: { description: 'Fetches the full details and status of a single Twilio message by its SID. Use to check delivery status or inspect a known message; requires the message SID (starting with "SM" or "MM"). Read-only and idempotent.', idempotent: !0 },
  displayName: "Get Message",
  props: {
    message_sid: import_pieces_framework6.Property.ShortText({
      displayName: "Message SID",
      description: 'The unique identifier (SID) of the message to retrieve. It starts with "SM" or "MM".',
      required: !0
    })
  },
  async run(context) {
    let { message_sid } = context.propsValue, account_sid = context.auth.username, auth_token = context.auth.password, path = `Messages/${message_sid}.json`;
    return (await callTwilioApi(
      import_pieces_common6.HttpMethod.GET,
      path,
      { account_sid, auth_token }
    )).body;
  }
});

// ../ap/packages/pieces/community/twilio/src/lib/action/download-recording-media.ts
var import_pieces_framework7 = require("@activepieces/pieces-framework"), import_pieces_common7 = require("@activepieces/pieces-common");
var twilioDownloadRecordingMedia = (0, import_pieces_framework7.createAction)({
  auth: twilioAuth,
  name: "download_recording_media",
  classification: "READ",
  displayName: "Download Recording Media",
  description: "Download the media file for a specific recording.",
  audience: "both",
  aiMetadata: { description: 'Downloads the audio file for a specific Twilio call recording by its SID, returning it as a file (MP3 or WAV). Use to retrieve recorded call audio for storage or further processing; requires the recording SID (starting with "RE"). Read-only and idempotent.', idempotent: !0 },
  props: {
    recording_sid: import_pieces_framework7.Property.ShortText({
      displayName: "Recording SID",
      description: 'The unique identifier (SID) of the recording to download. It starts with "RE".',
      required: !0
    }),
    format: import_pieces_framework7.Property.StaticDropdown({
      displayName: "Format",
      description: "The desired audio format for the download file.",
      required: !1,
      defaultValue: "mp3",
      options: {
        options: [
          { label: "MP3", value: "mp3" },
          { label: "WAV", value: "wav" }
        ]
      }
    }),
    channels: import_pieces_framework7.Property.StaticDropdown({
      displayName: "Channels",
      description: "Specify whether to download a mono or dual-channel file. Note: Dual-channel may not be available for all recordings.",
      required: !1,
      options: {
        options: [
          { label: "Mono", value: 1 },
          { label: "Dual", value: 2 }
        ]
      }
    })
  },
  async run(context) {
    let { recording_sid, format, channels } = context.propsValue, account_sid = context.auth.username, auth_token = context.auth.password, fileFormat = format != null ? format : "mp3", path = `Recordings/${recording_sid}.${fileFormat}`, queryParams = {};
    channels && (queryParams.RequestedChannels = channels.toString());
    let response = await import_pieces_common7.httpClient.sendRequest({
      method: import_pieces_common7.HttpMethod.GET,
      url: `https://api.twilio.com/2010-04-01/Accounts/${account_sid}/${path}`,
      queryParams,
      authentication: {
        type: import_pieces_common7.AuthenticationType.BASIC,
        username: account_sid,
        password: auth_token
      },
      responseType: "arraybuffer"
    }), fileData = Buffer.from(response.body);
    return await context.files.write({
      fileName: `${recording_sid}.${fileFormat}`,
      data: fileData
    });
  }
});

// ../ap/packages/pieces/community/twilio/src/lib/trigger/new-phone-number.ts
var import_pieces_framework8 = require("@activepieces/pieces-framework"), import_pieces_common8 = require("@activepieces/pieces-common");
var polling = {
  strategy: import_pieces_common8.DedupeStrategy.TIMEBASED,
  async items({ auth, lastFetchEpochMS }) {
    var _a, _b;
    let isTest = lastFetchEpochMS === 0, account_sid = auth.username, auth_token = auth.password, currentUri = `/2010-04-01/Accounts/${account_sid}/IncomingPhoneNumbers.json?PageSize=${isTest ? 10 : 1e3}`, results = [], stop = !1;
    do {
      let payload = (await import_pieces_common8.httpClient.sendRequest({
        method: import_pieces_common8.HttpMethod.GET,
        url: `https://api.twilio.com${currentUri}`,
        authentication: {
          type: import_pieces_common8.AuthenticationType.BASIC,
          username: account_sid,
          password: auth_token
        }
      })).body, numbers = (_a = payload.incoming_phone_numbers) != null ? _a : [];
      for (let num of numbers) {
        let ts = new Date(num.date_created).getTime();
        if (isTest || ts > lastFetchEpochMS)
          results.push(num);
        else {
          stop = !0;
          break;
        }
      }
      if (isTest) break;
      currentUri = (_b = payload == null ? void 0 : payload.next_page_uri) != null ? _b : null;
    } while (currentUri && !stop);
    return results.map((num) => ({
      epochMilliSeconds: new Date(num.date_created).getTime(),
      data: num
    }));
  }
}, twilioNewPhoneNumber = (0, import_pieces_framework8.createTrigger)({
  auth: twilioAuth,
  name: "new_phone_number",
  classification: "READ",
  displayName: "New Phone Number",
  description: "Triggers when you add a new phone number to your account.",
  aiMetadata: {
    description: "Fires when a new phone number is provisioned on the Twilio account. Each event represents one newly added incoming phone number with its capabilities and configuration."
  },
  props: {},
  sampleData: {
    sid: "PNXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXX",
    account_sid: "ACXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXX",
    friendly_name: "(555) 123-4567",
    phone_number: "+15551234567",
    voice_url: "https://demo.twilio.com/welcome/voice/",
    voice_method: "POST",
    sms_url: "https://demo.twilio.com/welcome/sms/",
    sms_method: "POST",
    date_created: "2025-08-28T11:44:10+00:00",
    date_updated: "2025-08-28T11:44:10+00:00",
    capabilities: {
      voice: !0,
      sms: !0,
      mms: !0
    },
    uri: "/2010-04-01/Accounts/ACXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXX/IncomingPhoneNumbers/PNXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXX.json"
  },
  type: import_pieces_framework8.TriggerStrategy.POLLING,
  async onEnable(context) {
    await import_pieces_common8.pollingHelper.onEnable(polling, {
      auth: context.auth,
      store: context.store,
      propsValue: context.propsValue
    });
  },
  async onDisable(context) {
    await import_pieces_common8.pollingHelper.onDisable(polling, {
      auth: context.auth,
      store: context.store,
      propsValue: context.propsValue
    });
  },
  async test(context) {
    return await import_pieces_common8.pollingHelper.test(polling, context);
  },
  async run(context) {
    return await import_pieces_common8.pollingHelper.poll(polling, context);
  }
});

// ../ap/packages/pieces/community/twilio/src/lib/trigger/new-recording.ts
var import_pieces_framework9 = require("@activepieces/pieces-framework"), import_pieces_common9 = require("@activepieces/pieces-common");
var polling2 = {
  strategy: import_pieces_common9.DedupeStrategy.TIMEBASED,
  async items({ auth, lastFetchEpochMS }) {
    var _a, _b;
    let isTest = lastFetchEpochMS === 0, account_sid = auth.username, auth_token = auth.password, currentUri = `/2010-04-01/Accounts/${account_sid}/Recordings.json?PageSize=${isTest ? 10 : 1e3}`, results = [], stop = !1;
    do {
      let payload = (await import_pieces_common9.httpClient.sendRequest({
        method: import_pieces_common9.HttpMethod.GET,
        url: `https://api.twilio.com${currentUri}`,
        authentication: {
          type: import_pieces_common9.AuthenticationType.BASIC,
          username: account_sid,
          password: auth_token
        }
      })).body, recordings = (_a = payload.recordings) != null ? _a : [];
      for (let recording of recordings) {
        let ts = new Date(recording.date_created).getTime();
        if (isTest || ts > lastFetchEpochMS)
          results.push(recording);
        else {
          stop = !0;
          break;
        }
      }
      if (isTest) break;
      currentUri = (_b = payload == null ? void 0 : payload.next_page_uri) != null ? _b : null;
    } while (currentUri && !stop);
    return results.map((recording) => ({
      epochMilliSeconds: new Date(recording.date_created).getTime(),
      data: recording
    }));
  }
}, twilioNewRecording = (0, import_pieces_framework9.createTrigger)({
  auth: twilioAuth,
  name: "new_recording",
  classification: "READ",
  displayName: "New Recording",
  description: "Triggers when a new call recording is completed and available.",
  aiMetadata: {
    description: "Fires when a new call recording becomes available on the Twilio account. Each event represents one completed recording, including its call SID, duration, and status."
  },
  props: {},
  sampleData: {
    account_sid: "ACaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
    api_version: "2010-04-01",
    call_sid: "CAaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
    conference_sid: "CFaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
    channels: 1,
    date_created: "Fri, 14 Oct 2016 21:56:34 +0000",
    date_updated: "Fri, 14 Oct 2016 21:56:38 +0000",
    start_time: "Fri, 14 Oct 2016 21:56:34 +0000",
    price: "0.04",
    price_unit: "USD",
    duration: "4",
    sid: "REaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
    source: "StartConferenceRecordingAPI",
    status: "completed",
    error_code: null,
    uri: "/2010-04-01/Accounts/ACaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa/Recordings/REaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa.json",
    subresource_uris: {
      add_on_results: "/2010-04-01/Accounts/ACaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa/Recordings/REaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa/AddOnResults.json",
      transcriptions: "/2010-04-01/Accounts/ACaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa/Recordings/REaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa/Transcriptions.json"
    },
    encryption_details: {
      encryption_public_key_sid: "CRaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
      encryption_cek: "OV4h6zrsxMIW7h0Zfqwfn6TI2GCNl54KALlg8wn8YB8KYZhXt6HlgvBWAmQTlfYVeLWydMiCewY0YkDDT1xmNe5huEo9vjuKBS5OmYK4CZkSx1NVv3XOGrZHpd2Pl/5WJHVhUK//AUO87uh5qnUP2E0KoLh1nyCLeGcEkXU0RfpPn/6nxjof/n6m6OzZOyeIRK4Oed5+rEtjqFDfqT0EVKjs6JAxv+f0DCc1xYRHl2yV8bahUPVKs+bHYdy4PVszFKa76M/Uae4jFA9Lv233JqWcxj+K2UoghuGhAFbV/JQIIswY2CBYI8JlVSifSqNEl9vvsTJ8bkVMm3MKbG2P7Q==",
      encryption_iv: "8I2hhNIYNTrwxfHk"
    },
    media_url: "http://api.twilio.com/2010-04-01/Accounts/ACaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa/Recordings/REaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
  },
  type: import_pieces_framework9.TriggerStrategy.POLLING,
  async onEnable(context) {
    await import_pieces_common9.pollingHelper.onEnable(polling2, {
      auth: context.auth,
      store: context.store,
      propsValue: context.propsValue
    });
  },
  async onDisable(context) {
    await import_pieces_common9.pollingHelper.onDisable(polling2, {
      auth: context.auth,
      store: context.store,
      propsValue: context.propsValue
    });
  },
  async test(context) {
    return await import_pieces_common9.pollingHelper.test(polling2, context);
  },
  async run(context) {
    return await import_pieces_common9.pollingHelper.poll(polling2, context);
  }
});

// ../ap/packages/pieces/community/twilio/src/lib/trigger/new-transcription.ts
var import_pieces_framework10 = require("@activepieces/pieces-framework"), import_pieces_common10 = require("@activepieces/pieces-common");
var polling3 = {
  strategy: import_pieces_common10.DedupeStrategy.TIMEBASED,
  async items({ auth, lastFetchEpochMS }) {
    var _a, _b;
    let isTest = lastFetchEpochMS === 0, account_sid = auth.username, auth_token = auth.password, currentUri = `/2010-04-01/Accounts/${account_sid}/Transcriptions.json?PageSize=${isTest ? 10 : 1e3}`, results = [], stop = !1;
    do {
      let payload = (await import_pieces_common10.httpClient.sendRequest({
        method: import_pieces_common10.HttpMethod.GET,
        url: `https://api.twilio.com${currentUri}`,
        authentication: {
          type: import_pieces_common10.AuthenticationType.BASIC,
          username: account_sid,
          password: auth_token
        }
      })).body, transcriptions = (_a = payload.transcriptions) != null ? _a : [];
      for (let recording of transcriptions) {
        let ts = new Date(recording.date_created).getTime();
        if (recording.status === "completed")
          if (isTest || ts > lastFetchEpochMS)
            results.push(recording);
          else {
            stop = !0;
            break;
          }
      }
      if (isTest) break;
      currentUri = (_b = payload == null ? void 0 : payload.next_page_uri) != null ? _b : null;
    } while (currentUri && !stop);
    return results.map((recording) => ({
      epochMilliSeconds: new Date(recording.date_created).getTime(),
      data: recording
    }));
  }
}, twilioNewTranscription = (0, import_pieces_framework10.createTrigger)({
  auth: twilioAuth,
  name: "new_transcription",
  classification: "READ",
  displayName: "New Transcription",
  description: "Triggers when a new call recording transcription is completed.",
  aiMetadata: {
    description: "Fires when a call recording transcription reaches completed status on the Twilio account. Each event represents one finished transcription tied to a recording, including its transcribed text."
  },
  props: {},
  sampleData: {
    account_sid: "ACaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
    api_version: "2008-08-01",
    date_created: "Thu, 25 Aug 2011 20:59:45 +0000",
    date_updated: "Thu, 25 Aug 2011 20:59:45 +0000",
    duration: "10",
    price: "0.00000",
    price_unit: "USD",
    recording_sid: "REaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
    sid: "TRaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
    status: "completed",
    transcription_text: null,
    type: "fast",
    uri: "/2010-04-01/Accounts/ACaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa/Transcriptions/TRaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa.json"
  },
  type: import_pieces_framework10.TriggerStrategy.POLLING,
  async onEnable(context) {
    await import_pieces_common10.pollingHelper.onEnable(polling3, {
      auth: context.auth,
      store: context.store,
      propsValue: context.propsValue
    });
  },
  async onDisable(context) {
    await import_pieces_common10.pollingHelper.onDisable(polling3, {
      auth: context.auth,
      store: context.store,
      propsValue: context.propsValue
    });
  },
  async test(context) {
    return await import_pieces_common10.pollingHelper.test(polling3, context);
  },
  async run(context) {
    return await import_pieces_common10.pollingHelper.poll(polling3, context);
  }
});

// ../ap/packages/pieces/community/twilio/src/lib/trigger/new-call.ts
var import_pieces_framework11 = require("@activepieces/pieces-framework");
var import_pieces_common11 = require("@activepieces/pieces-common"), polling4 = {
  strategy: import_pieces_common11.DedupeStrategy.TIMEBASED,
  async items({ auth, lastFetchEpochMS }) {
    var _a, _b;
    let isTest = lastFetchEpochMS === 0, account_sid = auth.username, auth_token = auth.password, currentUri = `/2010-04-01/Accounts/${account_sid}/Calls.json?PageSize=${isTest ? 1 : 1e3}`, results = [], stop = !1;
    do {
      let payload = (await import_pieces_common11.httpClient.sendRequest({
        method: import_pieces_common11.HttpMethod.GET,
        url: `https://api.twilio.com${currentUri}`,
        authentication: {
          type: import_pieces_common11.AuthenticationType.BASIC,
          username: account_sid,
          password: auth_token
        }
      })).body, calls = (_a = payload.calls) != null ? _a : [];
      for (let call of calls) {
        let ts = new Date(call.date_created).getTime();
        if (call.status === "completed")
          if (isTest || ts > lastFetchEpochMS)
            results.push(call);
          else {
            stop = !0;
            break;
          }
      }
      if (isTest) break;
      currentUri = (_b = payload == null ? void 0 : payload.next_page_uri) != null ? _b : null;
    } while (currentUri && !stop);
    return results.map((call) => ({
      epochMilliSeconds: new Date(call.date_created).getTime(),
      data: call
    }));
  }
}, twilioNewCall = (0, import_pieces_framework11.createTrigger)({
  auth: twilioAuth,
  name: "new_call",
  classification: "READ",
  displayName: "New Call",
  description: "Triggers when a call completes (incoming or outgoing).",
  aiMetadata: {
    description: "Fires when a voice call reaches completed status on the Twilio account, for both inbound and outbound calls. Each event represents one finished call with its direction, participants, duration, and status."
  },
  props: {},
  sampleData: {
    account_sid: "ACaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
    answered_by: "machine_start",
    api_version: "2010-04-01",
    caller_name: "callerid1",
    date_created: "Fri, 18 Oct 2019 17:00:00 +0000",
    date_updated: "Fri, 18 Oct 2019 17:01:00 +0000",
    direction: "outbound-api",
    duration: "4",
    end_time: "Fri, 18 Oct 2019 17:03:00 +0000",
    forwarded_from: "calledvia1",
    from: "+13051416799",
    from_formatted: "(305) 141-6799",
    group_sid: "GPdeadbeefdeadbeefdeadbeefdeadbeef",
    parent_call_sid: "CAdeadbeefdeadbeefdeadbeefdeadbeef",
    phone_number_sid: "PNdeadbeefdeadbeefdeadbeefdeadbeef",
    price: "-0.200",
    price_unit: "USD",
    sid: "CAaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
    start_time: "Fri, 18 Oct 2019 17:02:00 +0000",
    status: "completed",
    subresource_uris: {
      notifications: "/2010-04-01/Accounts/ACaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa/Calls/CAaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa/Notifications.json",
      recordings: "/2010-04-01/Accounts/ACaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa/Calls/CAaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa/Recordings.json",
      payments: "/2010-04-01/Accounts/ACaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa/Calls/CAaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa/Payments.json",
      events: "/2010-04-01/Accounts/ACaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa/Calls/CAaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa/Events.json",
      siprec: "/2010-04-01/Accounts/ACaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa/Calls/CAaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa/Siprec.json",
      streams: "/2010-04-01/Accounts/ACaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa/Calls/CAaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa/Streams.json",
      transcriptions: "/2010-04-01/Accounts/ACaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa/Calls/CAaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa/Transcriptions.json",
      user_defined_message_subscriptions: "/2010-04-01/Accounts/ACaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa/Calls/CAaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa/UserDefinedMessageSubscriptions.json",
      user_defined_messages: "/2010-04-01/Accounts/ACaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa/Calls/CAaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa/UserDefinedMessages.json"
    },
    to: "+13051913581",
    to_formatted: "(305) 191-3581",
    trunk_sid: "TKdeadbeefdeadbeefdeadbeefdeadbeef",
    uri: "/2010-04-01/Accounts/ACaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa/Calls/CAaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa.json",
    queue_time: "1000"
  },
  type: import_pieces_framework11.TriggerStrategy.POLLING,
  async onEnable(context) {
    await import_pieces_common11.pollingHelper.onEnable(polling4, {
      auth: context.auth,
      store: context.store,
      propsValue: context.propsValue
    });
  },
  async onDisable(context) {
    await import_pieces_common11.pollingHelper.onDisable(polling4, {
      auth: context.auth,
      store: context.store,
      propsValue: context.propsValue
    });
  },
  async test(context) {
    return await import_pieces_common11.pollingHelper.test(polling4, context);
  },
  async run(context) {
    return await import_pieces_common11.pollingHelper.poll(polling4, context);
  }
});

// ../ap/packages/pieces/community/twilio/src/index.ts
var twilioAuth = import_pieces_framework12.PieceAuth.BasicAuth({
  description: "The authentication to use to connect to Twilio",
  required: !0,
  username: {
    displayName: "Account SID",
    description: "The account SID to use to connect to Twilio"
  },
  password: {
    displayName: "Auth token",
    description: "The auth token to use to connect to Twilio"
  }
}), twilio = (0, import_pieces_framework12.createPiece)({
  displayName: "Twilio",
  description: "Cloud communications platform for building SMS, Voice & Messaging applications",
  minimumSupportedRelease: "0.30.0",
  logoUrl: "https://cdn.activepieces.com/pieces/twilio.png",
  auth: twilioAuth,
  categories: [import_pieces_framework13.PieceCategory.COMMUNICATION],
  actions: [
    twilioSendSms,
    twilioPhoneNumberLookup,
    twilioMakeCall,
    twilioGetMessage,
    twilioDownloadRecordingMedia,
    (0, import_pieces_common12.createCustomApiCallAction)({
      baseUrl: () => "https://api.twilio.com/2010-04-01",
      auth: twilioAuth,
      authMapping: async (auth) => ({
        Authorization: `Basic ${Buffer.from(
          `${auth.username}:${auth.password}`
        ).toString("base64")}`
      })
    })
  ],
  authors: ["kishanprmr", "MoShizzle", "khaledmashaly", "abuaboud"],
  triggers: [
    twilioNewIncomingSms,
    twilioNewPhoneNumber,
    twilioNewRecording,
    twilioNewTranscription,
    twilioNewCall
  ]
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  twilio,
  twilioAuth
});
