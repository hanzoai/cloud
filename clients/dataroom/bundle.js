// Hanzo Dataroom — goja bundle (read-WRITE, on clients/gojabase).
//
// SELF-CONTAINED, NO ESM, NO node: imports. The complete dataroom business
// logic (documents, data rooms, shareable links with access controls, viewers,
// per-page view analytics), authored to run verbatim inside the dop251/goja
// engine embedded in hanzoai/cloud (HIP-0106, task #101). It is the ESM-free
// port of the Papermark/Next.js API handlers this fold replaces — the Prisma
// data model becomes Base/SQLite tables (see the leaf's schema.go), the handlers
// become the route table below. No Postgres, no Next.js.
//
// Host contract (clients/gojabase injects these per dispatch; each dispatch runs
// inside ONE per-tenant SQLite transaction that commits iff status < 400):
//   globalThis.__db.query(sql, args)   -> [ {col: val, ...}, ... ]
//   globalThis.__db.exec(sql, args)    -> { changes, lastId }
//   globalThis.__newId()               -> collision-resistant id (crypto/rand)
//   globalThis.__now()                 -> unix milliseconds
//   globalThis.__bcrypt.hash(pw)       -> bcrypt hash   (leaf HostFn, vetted Go)
//   globalThis.__bcrypt.verify(pw,h)   -> bool
//   globalThis.handle({ route, params, query, orgId, body }) -> { status, body }
//
// Document BYTES live in object storage (the cloud VFS/S3 seam) and are handled
// by the Go leaf; this bundle only ever stores/reads the opaque storage KEY.
//
// CANONICAL SOURCE: github.com/hanzoai/dataroom → goja/bundle.js. This file is a
// byte-identical VENDORED copy that clients/dataroom go:embeds (the task mandates
// go:embed here rather than importing a Go module, since the dataroom repo is a
// TS app, not a Go module). Edit the canonical copy, then re-vendor here.
(function () {
  'use strict';

  // === tiny helpers ==========================================================

  function q(sql, args) { return globalThis.__db.query(sql, args || []); }
  function e(sql, args) { return globalThis.__db.exec(sql, args || []); }
  function id(prefix) { return (prefix || '') + globalThis.__newId(); }
  function now() { return globalThis.__now(); }
  function one(rows) { return rows && rows.length ? rows[0] : null; }

  function jsonList(v) {
    if (v == null) return [];
    try { var a = JSON.parse(v); return Array.isArray(a) ? a : []; }
    catch (_) { return []; }
  }
  function truthy(v) { return v === true || v === 1 || v === '1'; }
  function asInt(v, dflt) {
    if (v == null || v === '') return dflt == null ? null : dflt;
    var n = parseInt(v, 10);
    return isNaN(n) ? (dflt == null ? null : dflt) : n;
  }

  // err builds a route result carrying a non-200 status via __status. A >=400
  // status also rolls back the dispatch transaction (gojabase), so a rejected
  // request leaves the tenant DB untouched.
  function err(status, message) { return { __status: status, error: message }; }

  // === shapers (row -> API object) ===========================================

  function documentOut(r) {
    if (!r) return null;
    return {
      id: r.id, name: r.name, fileKey: r.file_key, contentType: r.content_type,
      type: r.type, numPages: r.num_pages, fileSize: r.file_size,
      createdAt: r.created_at, updatedAt: r.updated_at,
    };
  }
  function dataroomOut(r) {
    if (!r) return null;
    return {
      id: r.id, pId: r.p_id, name: r.name, description: r.description,
      createdAt: r.created_at, updatedAt: r.updated_at,
    };
  }
  function linkOut(r) {
    if (!r) return null;
    // password_hash is NEVER returned — only whether one is set.
    return {
      id: r.id, linkType: r.link_type, dataroomId: r.dataroom_id, documentId: r.document_id,
      name: r.name, emailProtected: truthy(r.email_protected), hasPassword: !!r.password_hash,
      allowList: jsonList(r.allow_list), allowDownload: truthy(r.allow_download),
      expiresAt: r.expires_at, isArchived: truthy(r.is_archived),
      createdAt: r.created_at, updatedAt: r.updated_at,
    };
  }

  // emailAllowed reports whether email passes the link allow list. An empty list
  // allows anyone (email gate only). Entries may be a full email ("a@b.com"), a
  // "@domain.com" suffix, or a bare "domain.com".
  function emailAllowed(email, allowList) {
    if (!allowList || !allowList.length) return true;
    if (!email) return false;
    var lc = String(email).toLowerCase().trim();
    var at = lc.lastIndexOf('@');
    var domain = at >= 0 ? lc.slice(at + 1) : '';
    for (var i = 0; i < allowList.length; i++) {
      var entry = String(allowList[i]).toLowerCase().trim();
      if (!entry) continue;
      if (entry.indexOf('@') === 0) { if (domain === entry.slice(1)) return true; }
      else if (entry.indexOf('@') > 0) { if (lc === entry) return true; }
      else if (domain === entry) return true;
    }
    return false;
  }

  function linkExpired(r) {
    return r.expires_at != null && Number(r.expires_at) > 0 && now() > Number(r.expires_at);
  }

  // === route handlers ========================================================
  // Admin routes are org-scoped by the per-tenant DB gojabase selects; the Go
  // leaf refuses any request without a validated principal before dispatching.
  // Viewer routes run under the org resolved from the public link id.

  var routes = {

    // ---- documents ----------------------------------------------------------
    // POST: the Go leaf has already stored the bytes on the object-storage seam
    // and passes the resulting fileKey; this records the metadata row.
    'documents.create': function (ctx) {
      var b = ctx.body || {};
      if (!b.name || !b.fileKey) return err(400, 'name and fileKey required');
      var docId = id('doc_');
      var t = now();
      e('INSERT INTO document (id,name,file_key,content_type,type,num_pages,file_size,created_at,updated_at) ' +
        'VALUES (?,?,?,?,?,?,?,?,?)',
        [docId, String(b.name), String(b.fileKey), b.contentType || null, b.type || null,
         asInt(b.numPages), asInt(b.fileSize), t, t]);
      return { document: documentOut(one(q('SELECT * FROM document WHERE id=?', [docId]))) };
    },
    'documents.list': function () {
      return { documents: q('SELECT * FROM document ORDER BY created_at DESC').map(documentOut) };
    },
    'documents.get': function (ctx) {
      var r = one(q('SELECT * FROM document WHERE id=?', [ctx.params.id]));
      if (!r) return err(404, 'document not found');
      return { document: documentOut(r) };
    },
    // documents.file returns the storage key so the Go leaf can stream the bytes.
    'documents.file': function (ctx) {
      var r = one(q('SELECT * FROM document WHERE id=?', [ctx.params.id]));
      if (!r) return err(404, 'document not found');
      return { fileKey: r.file_key, contentType: r.content_type, name: r.name };
    },

    // ---- datarooms ----------------------------------------------------------
    'datarooms.create': function (ctx) {
      var b = ctx.body || {};
      if (!b.name) return err(400, 'name required');
      var roomId = id('room_');
      var pId = 'dr_' + globalThis.__newId().slice(1, 13);
      var t = now();
      e('INSERT INTO dataroom (id,p_id,name,description,created_at,updated_at) VALUES (?,?,?,?,?,?)',
        [roomId, pId, String(b.name), b.description || null, t, t]);
      return { dataroom: dataroomOut(one(q('SELECT * FROM dataroom WHERE id=?', [roomId]))) };
    },
    'datarooms.list': function () {
      return { datarooms: q('SELECT * FROM dataroom ORDER BY created_at DESC').map(dataroomOut) };
    },
    'datarooms.get': function (ctx) {
      var r = one(q('SELECT * FROM dataroom WHERE id=?', [ctx.params.id]));
      if (!r) return err(404, 'dataroom not found');
      var docs = q(
        'SELECT dd.id AS dd_id, dd.order_index, doc.* FROM dataroom_document dd ' +
        'JOIN document doc ON doc.id = dd.document_id WHERE dd.dataroom_id=? ' +
        'ORDER BY dd.order_index IS NULL, dd.order_index, dd.created_at', [r.id]);
      var out = dataroomOut(r);
      out.documents = docs.map(function (d) {
        var doc = documentOut(d);
        doc.dataroomDocumentId = d.dd_id;
        doc.orderIndex = d.order_index;
        return doc;
      });
      return { dataroom: out };
    },
    'datarooms.addDocument': function (ctx) {
      var b = ctx.body || {};
      var roomId = ctx.params.id;
      var docId = b.documentId;
      if (!docId) return err(400, 'documentId required');
      if (!one(q('SELECT id FROM dataroom WHERE id=?', [roomId]))) return err(404, 'dataroom not found');
      if (!one(q('SELECT id FROM document WHERE id=?', [docId]))) return err(404, 'document not found');
      if (one(q('SELECT id FROM dataroom_document WHERE dataroom_id=? AND document_id=?', [roomId, docId]))) {
        return err(409, 'document already in dataroom');
      }
      var ddId = id('dd_');
      e('INSERT INTO dataroom_document (id,dataroom_id,document_id,order_index,created_at) VALUES (?,?,?,?,?)',
        [ddId, roomId, docId, asInt(b.orderIndex), now()]);
      return { dataroomDocumentId: ddId, dataroomId: roomId, documentId: docId };
    },

    // ---- links --------------------------------------------------------------
    'links.create': function (ctx) {
      var b = ctx.body || {};
      var roomId = b.dataroomId || null;
      var docId = b.documentId || null;
      if (!roomId && !docId) return err(400, 'dataroomId or documentId required');
      if (roomId && !one(q('SELECT id FROM dataroom WHERE id=?', [roomId]))) return err(404, 'dataroom not found');
      if (docId && !one(q('SELECT id FROM document WHERE id=?', [docId]))) return err(404, 'document not found');
      var pwHash = null;
      if (b.password) pwHash = globalThis.__bcrypt.hash(String(b.password));
      var allowList = Array.isArray(b.allowList) ? b.allowList : [];
      var denyList = Array.isArray(b.denyList) ? b.denyList : [];
      var linkId = id('link_');
      var t = now();
      var linkType = docId && !roomId ? 'DOCUMENT_LINK' : 'DATAROOM_LINK';
      e('INSERT INTO link (id,link_type,dataroom_id,document_id,name,password_hash,email_protected,' +
        'allow_list,deny_list,allow_download,expires_at,is_archived,created_at,updated_at) ' +
        'VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?)',
        [linkId, linkType, roomId, docId, b.name || null, pwHash,
         b.emailProtected === false ? 0 : 1, JSON.stringify(allowList), JSON.stringify(denyList),
         b.allowDownload ? 1 : 0, asInt(b.expiresAt), 0, t, t]);
      return { link: linkOut(one(q('SELECT * FROM link WHERE id=?', [linkId]))) };
    },
    'links.list': function () {
      return { links: q('SELECT * FROM link WHERE is_archived=0 ORDER BY created_at DESC').map(linkOut) };
    },

    // ---- viewer surface (public) -------------------------------------------
    // view.link returns the pre-auth metadata a visitor sees (what gates apply).
    'view.link': function (ctx) {
      var r = one(q('SELECT * FROM link WHERE id=?', [ctx.params.linkId]));
      if (!r || truthy(r.is_archived)) return err(404, 'link not found');
      var out = {
        id: r.id, name: r.name, linkType: r.link_type,
        emailProtected: truthy(r.email_protected), hasPassword: !!r.password_hash,
        allowDownload: truthy(r.allow_download), expired: linkExpired(r),
      };
      if (r.dataroom_id) {
        var room = one(q('SELECT * FROM dataroom WHERE id=?', [r.dataroom_id]));
        if (room) out.dataroom = { id: room.id, pId: room.p_id, name: room.name, description: room.description };
      }
      if (r.document_id) {
        var doc = one(q('SELECT * FROM document WHERE id=?', [r.document_id]));
        if (doc) out.document = { id: doc.id, name: doc.name, numPages: doc.num_pages };
      }
      return { link: out };
    },

    // view.authenticate enforces the access controls and, on success, records a
    // Viewer + a View (the analytics session) and returns the viewable payload.
    'view.authenticate': function (ctx) {
      var b = ctx.body || {};
      var r = one(q('SELECT * FROM link WHERE id=?', [ctx.params.linkId]));
      if (!r || truthy(r.is_archived)) return err(404, 'link not found');
      if (linkExpired(r)) return err(403, 'link expired');

      var email = b.email ? String(b.email).toLowerCase().trim() : '';
      if (truthy(r.email_protected) && !email) return err(401, 'email required');
      if (!emailAllowed(email, jsonList(r.allow_list))) return err(403, 'email not allowed');
      if (r.password_hash) {
        if (!b.password || !globalThis.__bcrypt.verify(String(b.password), r.password_hash)) {
          return err(401, 'invalid password');
        }
      }

      var viewerId = null;
      if (email) {
        var existing = one(q('SELECT * FROM viewer WHERE email=?', [email]));
        if (existing) { viewerId = existing.id; }
        else {
          viewerId = id('viewer_');
          var tv = now();
          e('INSERT INTO viewer (id,email,verified,created_at,updated_at) VALUES (?,?,?,?,?)',
            [viewerId, email, 0, tv, tv]);
        }
      }

      var viewId = id('view_');
      var viewType = r.dataroom_id ? 'DATAROOM_VIEW' : 'DOCUMENT_VIEW';
      e('INSERT INTO view (id,link_id,dataroom_id,document_id,viewer_id,viewer_email,view_type,verified,viewed_at) ' +
        'VALUES (?,?,?,?,?,?,?,?,?)',
        [viewId, r.id, r.dataroom_id, r.document_id, viewerId, email || null, viewType, 0, now()]);

      var payload = { viewId: viewId, viewerId: viewerId, allowDownload: truthy(r.allow_download) };
      if (r.dataroom_id) {
        var docs = q(
          'SELECT dd.id AS dd_id, doc.* FROM dataroom_document dd ' +
          'JOIN document doc ON doc.id = dd.document_id WHERE dd.dataroom_id=? ' +
          'ORDER BY dd.order_index IS NULL, dd.order_index, dd.created_at', [r.dataroom_id]);
        payload.documents = docs.map(function (d) {
          return { id: d.id, dataroomDocumentId: d.dd_id, name: d.name, numPages: d.num_pages };
        });
      } else if (r.document_id) {
        var single = one(q('SELECT * FROM document WHERE id=?', [r.document_id]));
        if (single) payload.document = { id: single.id, name: single.name, numPages: single.num_pages };
      }
      return payload;
    },

    // view.recordPage records ONE per-page analytics event (the page-by-page
    // tracking) against an existing View.
    'view.recordPage': function (ctx) {
      var b = ctx.body || {};
      var linkId = ctx.params.linkId;
      var v = one(q('SELECT * FROM view WHERE id=? AND link_id=?', [b.viewId, linkId]));
      if (!v) return err(404, 'view not found');
      if (b.pageNumber == null) return err(400, 'pageNumber required');
      var pvId = id('pv_');
      e('INSERT INTO page_view (id,view_id,link_id,document_id,dataroom_id,page_number,version_number,duration,viewed_at) ' +
        'VALUES (?,?,?,?,?,?,?,?,?)',
        [pvId, v.id, linkId, b.documentId || v.document_id, v.dataroom_id,
         asInt(b.pageNumber, 0), asInt(b.versionNumber), asInt(b.duration, 0), now()]);
      return { ok: true, id: pvId };
    },

    // view.file authorises a viewer download and returns the storage key.
    'view.file': function (ctx) {
      var linkId = ctx.params.linkId;
      var docId = ctx.params.documentId;
      var viewId = ctx.query.viewId;
      var r = one(q('SELECT * FROM link WHERE id=?', [linkId]));
      if (!r || truthy(r.is_archived)) return err(404, 'link not found');
      if (!one(q('SELECT id FROM view WHERE id=? AND link_id=?', [viewId, linkId]))) return err(403, 'no active view');
      if (truthy(ctx.query.download) && !truthy(r.allow_download)) return err(403, 'download disabled');
      // The document must be reachable through this link (dataroom membership or
      // the link's own document) — a viewer cannot address an unrelated doc.
      if (r.dataroom_id) {
        if (!one(q('SELECT id FROM dataroom_document WHERE dataroom_id=? AND document_id=?', [r.dataroom_id, docId]))) {
          return err(404, 'document not in dataroom');
        }
      } else if (r.document_id !== docId) {
        return err(404, 'document not on link');
      }
      var doc = one(q('SELECT * FROM document WHERE id=?', [docId]));
      if (!doc) return err(404, 'document not found');
      return { fileKey: doc.file_key, contentType: doc.content_type, name: doc.name };
    },

    // ---- analytics ----------------------------------------------------------
    'analytics.link': function (ctx) {
      var linkId = ctx.params.linkId;
      if (!one(q('SELECT id FROM link WHERE id=?', [linkId]))) return err(404, 'link not found');
      return linkAnalytics(linkId);
    },
    'analytics.dataroom': function (ctx) {
      var roomId = ctx.params.dataroomId;
      if (!one(q('SELECT id FROM dataroom WHERE id=?', [roomId]))) return err(404, 'dataroom not found');
      var links = q('SELECT id FROM link WHERE dataroom_id=?', [roomId]);
      var perLink = links.map(function (l) { return linkAnalytics(l.id); });
      var totalViews = 0, totalPageViews = 0;
      perLink.forEach(function (a) { totalViews += a.totalViews; totalPageViews += a.totalPageViews; });
      return { dataroomId: roomId, totalViews: totalViews, totalPageViews: totalPageViews, links: perLink };
    },
  };

  function linkAnalytics(linkId) {
    var totalViews = Number((one(q('SELECT COUNT(*) AS n FROM view WHERE link_id=?', [linkId])) || {}).n || 0);
    var totalPageViews = Number((one(q('SELECT COUNT(*) AS n FROM page_view WHERE link_id=?', [linkId])) || {}).n || 0);
    var pages = q(
      'SELECT page_number, COUNT(*) AS views, SUM(duration) AS total_duration ' +
      'FROM page_view WHERE link_id=? GROUP BY page_number ORDER BY page_number', [linkId]);
    return {
      linkId: linkId, totalViews: totalViews, totalPageViews: totalPageViews,
      pages: pages.map(function (p) {
        var v = Number(p.views || 0), d = Number(p.total_duration || 0);
        return { pageNumber: Number(p.page_number), views: v, totalDuration: d, avgDuration: v ? Math.round(d / v) : 0 };
      }),
    };
  }

  // === dispatch entry (host calls this per request) ==========================
  globalThis.handle = function (req) {
    req = req || {};
    var ctx = {
      params: req.params || {},
      query: req.query || {},
      orgId: req.orgId || '',
      body: req.body || null,
    };
    var fn = routes[req.route];
    if (!fn) return { status: 404, body: { error: 'unknown dataroom route: ' + req.route } };
    try {
      var out = fn(ctx);
      if (out && out.__status) {
        var status = out.__status;
        delete out.__status;
        return { status: status, body: out };
      }
      return { status: 200, body: out };
    } catch (ex) {
      return { status: 500, body: { error: String(ex && ex.message || ex) } };
    }
  };
})();
