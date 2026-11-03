// Gateway TURN injection for Telegram Web calls (served at /tg/_gw/rtc.js and
// loaded before the app bundle, see turn.go).
//
// Call media is WebRTC and cannot reach Telegram's own TURN servers from the
// client networks the gateway serves. This script wraps RTCPeerConnection so
// every connection also gets the gateway relay (turns:...:443), whose
// short-lived credentials come from /tg/_gw/turn for the logged-in user.
(function () {
  'use strict';

  var Orig = window.RTCPeerConnection;
  if (!Orig || Orig.__gatewayTurn) return;

  var ENDPOINT = '/tg/_gw/turn';
  var REFRESH_MS = 10 * 60 * 1000;
  var cached = null; // { iceServers, relayOnly, expiresAt }

  function store(text) {
    try {
      var data = JSON.parse(text);
      if (!data || !data.iceServers || !data.iceServers.length) return;
      cached = {
        iceServers: data.iceServers,
        relayOnly: !!data.relayOnly,
        expiresAt: Date.now() + data.ttl * 1000
      };
    } catch (e) { /* keep the previous credentials */ }
  }

  function fresh() {
    return cached && cached.expiresAt - 60 * 1000 > Date.now();
  }

  function refresh() {
    fetch(ENDPOINT, { credentials: 'same-origin', cache: 'no-store' })
      .then(function (r) { return r.ok ? r.text() : null; })
      .then(function (t) { if (t) store(t); })
      .catch(function () {});
  }

  // RTCPeerConnection is constructed synchronously, so when the credentials
  // are missing or stale at that moment they are fetched synchronously.
  function loadNow() {
    try {
      var xhr = new XMLHttpRequest();
      xhr.open('GET', ENDPOINT, false);
      xhr.send();
      if (xhr.status === 200) store(xhr.responseText);
    } catch (e) { /* the call proceeds with Telegram's own servers only */ }
  }

  function withGateway(config) {
    if (!fresh()) loadNow();
    if (!fresh()) return config;
    var out = {};
    var key;
    for (key in config || {}) out[key] = config[key];
    out.iceServers = cached.iceServers.concat((config && config.iceServers) || []);
    if (cached.relayOnly) out.iceTransportPolicy = 'relay';
    return out;
  }

  function RTCPeerConnection(config, constraints) {
    return arguments.length > 1
      ? new Orig(withGateway(config), constraints)
      : new Orig(withGateway(config));
  }
  RTCPeerConnection.prototype = Orig.prototype;
  Object.setPrototypeOf(RTCPeerConnection, Orig); // static generateCertificate
  RTCPeerConnection.__gatewayTurn = true;

  var setConfiguration = Orig.prototype.setConfiguration;
  if (setConfiguration) {
    Orig.prototype.setConfiguration = function (config) {
      return setConfiguration.call(this, withGateway(config));
    };
  }

  window.RTCPeerConnection = RTCPeerConnection;
  if (window.webkitRTCPeerConnection) window.webkitRTCPeerConnection = RTCPeerConnection;

  refresh();
  setInterval(refresh, REFRESH_MS);
})();
