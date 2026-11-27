"use strict";
var __defProp = Object.defineProperty;
var __getOwnPropDesc = Object.getOwnPropertyDescriptor;
var __getOwnPropNames = Object.getOwnPropertyNames;
var __hasOwnProp = Object.prototype.hasOwnProperty;
var __defNormalProp = (obj, key, value) => key in obj ? __defProp(obj, key, { enumerable: !0, configurable: !0, writable: !0, value }) : obj[key] = value;
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
var __publicField = (obj, key, value) => __defNormalProp(obj, typeof key != "symbol" ? key + "" : key, value);

// ../ap/packages/pieces/community/pastebin/src/index.ts
var index_exports = {};
__export(index_exports, {
  pastebin: () => pastebin,
  pastebinAuth: () => pastebinAuth
});
module.exports = __toCommonJS(index_exports);
var import_pieces_framework4 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/pastebin/src/lib/actions/create-paste.ts
var import_pieces_framework2 = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/pastebin/src/lib/common/index.ts
var import_pieces_framework = require("@activepieces/pieces-framework");

// ../ap/packages/pieces/community/pastebin/src/lib/common/client.ts
var import_pieces_common = require("@activepieces/pieces-common");
var PastebinClient = class {
  constructor(token) {
    __publicField(this, "token", token);
    __publicField(this, "user_key");
  }
  setUserKey(user_key) {
    this.user_key = user_key;
  }
  async makeRequest(script, option, body) {
    let req = new URLSearchParams({
      api_option: option,
      api_dev_key: this.token
    });
    return this.user_key && req.append("api_user_key", this.user_key), Object.keys(body).filter((k) => body[k] !== void 0 && body[k] !== null).forEach((k) => req.append("api_" + k, body[k])), (await import_pieces_common.httpClient.sendRequest({
      method: import_pieces_common.HttpMethod.POST,
      url: "https://pastebin.com/api/" + script,
      headers: {
        "content-type": "application/x-www-form-urlencoded"
      },
      body: req,
      responseType: "text"
    })).body;
  }
  async login(username, password) {
    return await this.makeRequest("api_login.php", "login", {
      user_name: username,
      user_password: password
    });
  }
  async createPaste(request) {
    return await this.makeRequest("api_post.php", "paste", request);
  }
  async getPasteContent(id) {
    return this.user_key ? await this.makeRequest("api_raw.php", "show_paste", {
      paste_key: id
    }) : (await import_pieces_common.httpClient.sendRequest({
      method: import_pieces_common.HttpMethod.GET,
      url: "https://pastebin.com/raw/" + id,
      responseType: "text"
    })).body;
  }
};

// ../ap/packages/pieces/community/pastebin/src/lib/common/index.ts
var pastebinCommon = {
  paste_format: (required = !0) => import_pieces_framework.Property.StaticDropdown({
    displayName: "Format",
    required,
    options: {
      options: [
        { label: "4CS4cs", value: "4cs" },
        { label: "6502 ACME Cross Asse...", value: "6502acme" },
        { label: "6502 Kick Assembler", value: "6502kickass" },
        { label: "6502 TASM/64TASS", value: "6502tasm" },
        { label: "ABAP", value: "abap" },
        { label: "ActionScript", value: "actionscript" },
        { label: "ActionScript 3", value: "actionscript3" },
        { label: "Ada", value: "ada" },
        { label: "AIMMS", value: "aimms" },
        { label: "ALGOL 68", value: "algol68" },
        { label: "Apache Log", value: "apache" },
        { label: "AppleScript", value: "applescript" },
        { label: "APT Sources", value: "apt_sources" },
        { label: "Arduino", value: "arduino" },
        { label: "ARM", value: "arm" },
        { label: "ASM (NASM)", value: "asm" },
        { label: "ASP", value: "asp" },
        { label: "Asymptote", value: "asymptote" },
        { label: "autoconf", value: "autoconf" },
        { label: "Autohotkey", value: "autohotkey" },
        { label: "AutoIt", value: "autoit" },
        { label: "Avisynth", value: "avisynth" },
        { label: "Awk", value: "awk" },
        { label: "BASCOM AVR", value: "bascomavr" },
        { label: "Bash", value: "bash" },
        { label: "Basic4GL", value: "basic4gl" },
        { label: "Batch", value: "dos" },
        { label: "BibTeX", value: "bibtex" },
        { label: "Blitz3D", value: "b3d" },
        { label: "Blitz Basic", value: "blitzbasic" },
        { label: "BlitzMax", value: "bmx" },
        { label: "BNF", value: "bnf" },
        { label: "BOO", value: "boo" },
        { label: "BrainFuck", value: "bf" },
        { label: "C", value: "c" },
        { label: "C#", value: "csharp" },
        { label: "C (WinAPI)", value: "c_winapi" },
        { label: "C++", value: "cpp" },
        { label: "C++ (WinAPI)", value: "cpp-winapi" },
        { label: "C++ (with Qt extensi...", value: "cpp-qt" },
        { label: "C: Loadrunner", value: "c_loadrunner" },
        { label: "CAD DCL", value: "caddcl" },
        { label: "CAD Lisp", value: "cadlisp" },
        { label: "Ceylon", value: "ceylon" },
        { label: "CFDG", value: "cfdg" },
        { label: "C for Macs", value: "c_mac" },
        { label: "ChaiScript", value: "chaiscript" },
        { label: "Chapel", value: "chapel" },
        { label: "C Intermediate Langu...", value: "cil" },
        { label: "Clojure", value: "clojure" },
        { label: "Clone C", value: "klonec" },
        { label: "Clone C++", value: "klonecpp" },
        { label: "CMake", value: "cmake" },
        { label: "COBOL", value: "cobol" },
        { label: "CoffeeScript", value: "coffeescript" },
        { label: "ColdFusion", value: "cfm" },
        { label: "CSS", value: "css" },
        { label: "Cuesheet", value: "cuesheet" },
        { label: "D", value: "d" },
        { label: "Dart", value: "dart" },
        { label: "DCL", value: "dcl" },
        { label: "DCPU-16", value: "dcpu16" },
        { label: "DCS", value: "dcs" },
        { label: "Delphi", value: "delphi" },
        { label: "Delphi Prism (Oxygen...", value: "oxygene" },
        { label: "Diff", value: "diff" },
        { label: "DIV", value: "div" },
        { label: "DOT", value: "dot" },
        { label: "E", value: "e" },
        { label: "Easytrieve", value: "ezt" },
        { label: "ECMAScript", value: "ecmascript" },
        { label: "Eiffel", value: "eiffel" },
        { label: "Email", value: "email" },
        { label: "EPC", value: "epc" },
        { label: "Erlang", value: "erlang" },
        { label: "Euphoria", value: "euphoria" },
        { label: "F#", value: "fsharp" },
        { label: "Falcon", value: "falcon" },
        { label: "Filemaker", value: "filemaker" },
        { label: "FO Language", value: "fo" },
        { label: "Formula One", value: "f1" },
        { label: "Fortran", value: "fortran" },
        { label: "FreeBasic", value: "freebasic" },
        { label: "FreeSWITCH", value: "freeswitch" },
        { label: "GAMBAS", value: "gambas" },
        { label: "Game Maker", value: "gml" },
        { label: "GDB", value: "gdb" },
        { label: "GDScript", value: "gdscript" },
        { label: "Genero", value: "genero" },
        { label: "Genie", value: "genie" },
        { label: "GetText", value: "gettext" },
        { label: "Go", value: "go" },
        { label: "Godot GLSL", value: "godot-glsl" },
        { label: "Groovy", value: "groovy" },
        { label: "GwBasic", value: "gwbasic" },
        { label: "Haskell", value: "haskell" },
        { label: "Haxe", value: "haxe" },
        { label: "HicEst", value: "hicest" },
        { label: "HQ9 Plus", value: "hq9plus" },
        { label: "HTML", value: "html4strict" },
        { label: "HTML 5", value: "html5" },
        { label: "Icon", value: "icon" },
        { label: "IDL", value: "idl" },
        { label: "INI file", value: "ini" },
        { label: "Inno Script", value: "inno" },
        { label: "INTERCAL", value: "intercal" },
        { label: "IO", value: "io" },
        { label: "ISPF Panel Definition...", value: "ispfpanel" },
        { label: "J", value: "j" },
        { label: "Java", value: "java" },
        { label: "Java 5", value: "java5" },
        { label: "JavaScript", value: "javascript" },
        { label: "JCL", value: "jcl" },
        { label: "jQuery", value: "jquery" },
        { label: "JSON", value: "json" },
        { label: "Julia", value: "julia" },
        { label: "KiXtart", value: "kixtart" },
        { label: "Kotlin", value: "kotlin" },
        { label: "KSP (Kontakt Script)", value: "ksp" },
        { label: "Latex", value: "latex" },
        { label: "LDIF", value: "ldif" },
        { label: "Liberty BASIC", value: "lb" },
        { label: "Linden Scripting", value: "lsl2" },
        { label: "Lisp", value: "lisp" },
        { label: "LLVM", value: "llvm" },
        { label: "Loco Basic", value: "locobasic" },
        { label: "Logtalk", value: "logtalk" },
        { label: "LOL Code", value: "lolcode" },
        { label: "Lotus Formulas", value: "lotusformulas" },
        { label: "Lotus Script", value: "lotusscript" },
        { label: "LScript", value: "lscript" },
        { label: "Lua", value: "lua" },
        { label: "M68000 Assembler", value: "m68k" },
        { label: "MagikSF", value: "magiksf" },
        { label: "Make", value: "make" },
        { label: "MapBasic", value: "mapbasic" },
        { label: "Markdown", value: "markdown" },
        { label: "MatLab", value: "matlab" },
        { label: "Mercury", value: "mercury" },
        { label: "MetaPost", value: "metapost" },
        { label: "mIRC", value: "mirc" },
        { label: "MIX Assembler", value: "mmix" },
        { label: "MK-61/52", value: "mk-61" },
        { label: "Modula 2", value: "modula2" },
        { label: "Modula 3", value: "modula3" },
        { label: "Motorola 68000 HiSof...", value: "68000devpac" },
        { label: "MPASM", value: "mpasm" },
        { label: "MXML", value: "mxml" },
        { label: "MySQL", value: "mysql" },
        { label: "Nagios", value: "nagios" },
        { label: "NetRexx", value: "netrexx" },
        { label: "newLISP", value: "newlisp" },
        { label: "Nginx", value: "nginx" },
        { label: "Nim", value: "nim" },
        { label: "NullSoft Installer", value: "nsis" },
        { label: "Oberon 2", value: "oberon2" },
        { label: "Objeck Programming L...", value: "objeck" },
        { label: "Objective C", value: "objc" },
        { label: "OCaml", value: "ocaml" },
        { label: "OCaml Brief", value: "ocaml-brief" },
        { label: "Octave", value: "octave" },
        { label: "OpenBSD PACKET FILTE...", value: "pf" },
        { label: "OpenGL Shading", value: "glsl" },
        { label: "Open Object Rexx", value: "oorexx" },
        { label: "Openoffice BASIC", value: "oobas" },
        { label: "Oracle 8", value: "oracle8" },
        { label: "Oracle 11", value: "oracle11" },
        { label: "Oz", value: "oz" },
        { label: "ParaSail", value: "parasail" },
        { label: "PARI/GP", value: "parigp" },
        { label: "Pascal", value: "pascal" },
        { label: "Pawn", value: "pawn" },
        { label: "PCRE", value: "pcre" },
        { label: "Per", value: "per" },
        { label: "Perl", value: "perl" },
        { label: "Perl 6", value: "perl6" },
        { label: "Phix", value: "phix" },
        { label: "PHP", value: "php" },
        { label: "PHP Brief", value: "php-brief" },
        { label: "Pic 16", value: "pic16" },
        { label: "Pike", value: "pike" },
        { label: "Pixel Bender", value: "pixelbender" },
        { label: "PL/I", value: "pli" },
        { label: "PL/SQL", value: "plsql" },
        { label: "PostgreSQL", value: "postgresql" },
        { label: "PostScript", value: "postscript" },
        { label: "POV-Ray", value: "povray" },
        { label: "PowerBuilder", value: "powerbuilder" },
        { label: "PowerShell", value: "powershell" },
        { label: "ProFTPd", value: "proftpd" },
        { label: "Progress", value: "progress" },
        { label: "Prolog", value: "prolog" },
        { label: "Properties", value: "properties" },
        { label: "ProvideX", value: "providex" },
        { label: "Puppet", value: "puppet" },
        { label: "PureBasic", value: "purebasic" },
        { label: "PyCon", value: "pycon" },
        { label: "Python", value: "python" },
        { label: "Python for S60", value: "pys60" },
        { label: "q/kdb+", value: "q" },
        { label: "QBasic", value: "qbasic" },
        { label: "QML", value: "qml" },
        { label: "R", value: "rsplus" },
        { label: "Racket", value: "racket" },
        { label: "Rails", value: "rails" },
        { label: "RBScript", value: "rbs" },
        { label: "REBOL", value: "rebol" },
        { label: "REG", value: "reg" },
        { label: "Rexx", value: "rexx" },
        { label: "Robots", value: "robots" },
        { label: "Roff Manpage", value: "roff" },
        { label: "RPM Spec", value: "rpmspec" },
        { label: "Ruby", value: "ruby" },
        { label: "Ruby Gnuplot", value: "gnuplot" },
        { label: "Rust", value: "rust" },
        { label: "SAS", value: "sas" },
        { label: "Scala", value: "scala" },
        { label: "Scheme", value: "scheme" },
        { label: "Scilab", value: "scilab" },
        { label: "SCL", value: "scl" },
        { label: "SdlBasic", value: "sdlbasic" },
        { label: "Smalltalk", value: "smalltalk" },
        { label: "Smarty", value: "smarty" },
        { label: "SPARK", value: "spark" },
        { label: "SPARQL", value: "sparql" },
        { label: "SQF", value: "sqf" },
        { label: "SQL", value: "sql" },
        { label: "SSH Config", value: "sshconfig" },
        { label: "StandardML", value: "standardml" },
        { label: "StoneScript", value: "stonescript" },
        { label: "SuperCollider", value: "sclang" },
        { label: "Swift", value: "swift" },
        { label: "SystemVerilog", value: "systemverilog" },
        { label: "T-SQL", value: "tsql" },
        { label: "TCL", value: "tcl" },
        { label: "Tera Term", value: "teraterm" },
        { label: "TeXgraph", value: "texgraph" },
        { label: "thinBasic", value: "thinbasic" },
        { label: "TypeScript", value: "typescript" },
        { label: "TypoScript", value: "typoscript" },
        { label: "Unicon", value: "unicon" },
        { label: "UnrealScript", value: "uscript" },
        { label: "UPC", value: "upc" },
        { label: "Urbi", value: "urbi" },
        { label: "Vala", value: "vala" },
        { label: "VB.NET", value: "vbnet" },
        { label: "VBScript", value: "vbscript" },
        { label: "Vedit", value: "vedit" },
        { label: "VeriLog", value: "verilog" },
        { label: "VHDL", value: "vhdl" },
        { label: "VIM", value: "vim" },
        { label: "VisualBasic", value: "vb" },
        { label: "VisualFoxPro", value: "visualfoxpro" },
        { label: "Visual Pro Log", value: "visualprolog" },
        { label: "WhiteSpace", value: "whitespace" },
        { label: "WHOIS", value: "whois" },
        { label: "Winbatch", value: "winbatch" },
        { label: "XBasic", value: "xbasic" },
        { label: "XML", value: "xml" },
        { label: "Xojo", value: "xojo" },
        { label: "Xorg Config", value: "xorg_conf" },
        { label: "XPP", value: "xpp" },
        { label: "YAML", value: "yaml" },
        { label: "YARA", value: "yara" },
        { label: "Z80 Assembler", value: "z80" },
        { label: "ZXBasic", value: "zxbasic" }
      ]
    }
  })
};
async function makeClient(auth) {
  let client = new PastebinClient(auth.props.token);
  if (auth.props.username && auth.props.password) {
    let userKey = await client.login(auth.props.username, auth.props.password);
    client.setUserKey(userKey);
  }
  return client;
}

// ../ap/packages/pieces/community/pastebin/src/lib/actions/create-paste.ts
var create_paste_default = (0, import_pieces_framework2.createAction)({
  auth: pastebinAuth,
  name: "create_paste",
  displayName: "Create Paste",
  description: "Creates a new paste",
  audience: "both",
  aiMetadata: { description: "Creates a new Pastebin paste from the given text content, returning its id and URL. Use to publish or share a snippet; optionally set a syntax format, name, expiry, privacy (public/unlisted/private), and folder. Not idempotent \u2014 each call creates a separate paste even with identical content.", idempotent: !1 },
  props: {
    content: import_pieces_framework2.Property.LongText({
      displayName: "Content",
      required: !0
    }),
    format: pastebinCommon.paste_format(!1),
    name: import_pieces_framework2.Property.ShortText({
      displayName: "Name",
      required: !1
    }),
    privacy: import_pieces_framework2.Property.StaticDropdown({
      displayName: "Privacy",
      required: !1,
      options: {
        options: [
          { label: "Public", value: "0" /* PUBLIC */ },
          { label: "Unlisted", value: "1" /* UNLISTED */ },
          { label: "Private", value: "2" /* PRIVATE */ }
        ]
      }
    }),
    expiry: import_pieces_framework2.Property.StaticDropdown({
      displayName: "Expiry",
      required: !1,
      options: {
        options: [
          { label: "Never", value: "N" /* NEVER */ },
          { label: "10 Minutes", value: "10M" /* TEN_MINUTES */ },
          { label: "1 Hour", value: "1H" /* ONE_HOUR */ },
          { label: "1 Day", value: "1D" /* ONE_DAY */ },
          { label: "1 Week", value: "1W" /* ONE_WEEK */ },
          { label: "2 Weeks", value: "2W" /* TWO_WEEKS */ },
          { label: "1 Month", value: "1M" /* ONE_MONTH */ },
          { label: "6 Months", value: "6M" /* SIX_MONTHS */ },
          { label: "1 Year", value: "1Y" /* ONE_YEAR */ }
        ]
      }
    }),
    folder: import_pieces_framework2.Property.ShortText({
      displayName: "Folder",
      required: !1
    })
  },
  async run(context) {
    let url = await (await makeClient(context.auth)).createPaste({
      paste_code: context.propsValue.content,
      paste_format: context.propsValue.format,
      paste_name: context.propsValue.name,
      paste_private: context.propsValue.privacy,
      paste_expiry_date: context.propsValue.expiry,
      folder_key: context.propsValue.folder
    });
    return {
      id: url.split("/").reduce((c, v) => v),
      url
    };
  }
});

// ../ap/packages/pieces/community/pastebin/src/lib/actions/get-paste-content.ts
var import_pieces_framework3 = require("@activepieces/pieces-framework");
var get_paste_content_default = (0, import_pieces_framework3.createAction)({
  auth: pastebinAuth,
  name: "get_paste_content",
  displayName: "Get Paste Content",
  description: "Retrieves the content of a paste",
  audience: "both",
  aiMetadata: { description: "Fetches the raw text content of an existing Pastebin paste by its paste ID. Use to read back a snippet you have the ID for; private pastes require account credentials on the connection. Idempotent \u2014 a read-only lookup with no side effect.", idempotent: !0 },
  props: {
    paste_id: import_pieces_framework3.Property.ShortText({
      displayName: "Paste ID",
      required: !0
    })
  },
  async run(context) {
    return {
      content: await (await makeClient(context.auth)).getPasteContent(context.propsValue.paste_id)
    };
  }
});

// ../ap/packages/pieces/community/pastebin/src/lib/actions/index.ts
var actions_default = [create_paste_default, get_paste_content_default];

// ../ap/packages/pieces/community/pastebin/src/index.ts
var markdownDescription = `
Here are the simple steps to get your credentials:

1. Make an account, If you don't have one yet.
2. Go to **https://pastebin.com/doc_api**.
3. Copy your unique Developer API Key and paste it.
4. Provide your username and password if you want to create **private pastes** under your account.
`, pastebinAuth = import_pieces_framework4.PieceAuth.CustomAuth({
  required: !0,
  description: markdownDescription,
  props: {
    token: import_pieces_framework4.PieceAuth.SecretText({
      displayName: "Developer Key",
      required: !0
    }),
    username: import_pieces_framework4.Property.ShortText({
      displayName: "Username",
      required: !1
    }),
    password: import_pieces_framework4.PieceAuth.SecretText({
      displayName: "Password",
      required: !1
    })
  }
}), pastebin = (0, import_pieces_framework4.createPiece)({
  displayName: "Pastebin",
  description: "Simple and secure text sharing",
  minimumSupportedRelease: "0.30.0",
  logoUrl: "https://cdn.activepieces.com/pieces/pastebin.png",
  authors: ["JanHolger", "kishanprmr", "khaledmashaly", "abuaboud"],
  categories: [],
  auth: pastebinAuth,
  actions: actions_default,
  triggers: []
});
// Annotate the CommonJS export names for ESM import in node:
0 && (module.exports = {
  pastebin,
  pastebinAuth
});
