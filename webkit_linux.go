//go:build linux

package main

/*
#cgo linux pkg-config: gtk+-3.0
#cgo !webkit2_41 pkg-config: webkit2gtk-4.0
#cgo webkit2_41 pkg-config: webkit2gtk-4.1
#include <string.h>
#include <gtk/gtk.h>
#include <webkit2/webkit2.h>

static WebKitWebView *airwavesView;
static double airwavesZoom = 1;
static gboolean airwavesKeepDamage;

// WebKitGTK 2.54 tracks what changed in each frame and draws only that onto
// a buffer it reuses. Under gamescope (Xwayland) parts of older frames show
// through: the guide's previous selection under the new one, a highlight
// left half drawn. Without damage tracking every frame is drawn whole,
// which the GPU does easily.
//
// The feature has to be off before the page is created, which is at the
// first load, so it is turned off when Wails puts the web view in its
// window, just before that load. The interface size is set there too.
static gboolean airwavesWebViewParented(GSignalInvocationHint *hint, guint n, const GValue *params, gpointer data) {
	GObject *widget = g_value_get_object(&params[0]);
	if (!WEBKIT_IS_WEB_VIEW(widget))
		return TRUE;
	airwavesView = WEBKIT_WEB_VIEW(widget);
	webkit_web_view_set_zoom_level(airwavesView, airwavesZoom);
	if (airwavesKeepDamage)
		return FALSE;
#if WEBKIT_CHECK_VERSION(2, 42, 0)
	WebKitSettings *settings = webkit_web_view_get_settings(airwavesView);
	WebKitFeatureList *features = webkit_settings_get_all_features();
	for (gsize i = 0; i < webkit_feature_list_get_length(features); i++) {
		WebKitFeature *feature = webkit_feature_list_get(features, i);
		if (!strcmp(webkit_feature_get_identifier(feature), "PropagateDamagingInformation")) {
			webkit_settings_set_feature_enabled(settings, feature, FALSE);
			g_message("airwaves: WebKit damage tracking off");
		}
	}
	webkit_feature_list_unref(features);
#endif
	return FALSE;
}

// Runs before gtk_init, which Wails calls later: loading GtkWidget's class
// (which needs no display) makes its signals known.
static void airwavesWatchWebView(gboolean keepDamage) {
	airwavesKeepDamage = keepDamage;
	g_type_class_ref(GTK_TYPE_WIDGET);
	g_signal_add_emission_hook(g_signal_lookup("parent-set", GTK_TYPE_WIDGET), 0, airwavesWebViewParented, NULL, NULL);
}

static gboolean airwavesApplyZoom(gpointer data) {
	if (airwavesView)
		webkit_web_view_set_zoom_level(airwavesView, airwavesZoom);
	return G_SOURCE_REMOVE;
}

// The web view is GTK's, so it is zoomed on GTK's main loop.
static void airwavesSetZoom(double zoom) {
	airwavesZoom = zoom;
	g_idle_add(airwavesApplyZoom, NULL);
}
*/
import "C"

import "os"

// nativeZoom: the interface size is WebKitGTK's page zoom, which keeps
// viewport units, fixed elements and video right; CSS zoom on the root
// misplaces them there.
const nativeZoom = true

// AIRWAVES_WEBKIT_DAMAGE=1 keeps WebKit's damage tracking, for comparison.
func init() {
	keep := C.gboolean(0)
	if os.Getenv("AIRWAVES_WEBKIT_DAMAGE") != "" {
		keep = 1
	}
	C.airwavesWatchWebView(keep)
}

// setPageZoom sizes the page: 1.25 is 125%.
func setPageZoom(zoom float64) { C.airwavesSetZoom(C.double(zoom)) }
