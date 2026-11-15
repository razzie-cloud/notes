package app

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"net/url"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/razzie-cloud/notes/internal/config"
	"github.com/razzie-cloud/notes/internal/store"
	"github.com/razzie-cloud/notes/tests/browser"
)

func TestNotesNavigationBrowser(t *testing.T) {
	if os.Getenv("NOTES_BROWSER_TEST") != "1" {
		t.Skip("set NOTES_BROWSER_TEST=1 and CHROMIUM_EXECUTABLE_PATH for local browser regression")
	}
	check := func(err error) {
		if err != nil {
			panic(err)
		}
	}
	a, err := New(config.Config{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	var pages, previews atomic.Int32
	var failNext atomic.Int32
	type heldRequest struct{ started, release chan struct{} }
	var holdHistory atomic.Pointer[heldRequest]
	c := store.Connection{ID: "connection-id", Name: "Fixture S3"}
	books := []store.Notebook{{ID: "book-one", Name: "First notebook"}, {ID: "book-two", Name: "Second notebook"}}
	b := browser.Start(func(r browser.Request) browser.Response {
		u, err := url.Parse(r.URL)
		check(err)
		if strings.HasPrefix(u.Path, "/static/") {
			return browser.Static(u.Path)
		}
		if u.Path == "/preview" {
			previews.Add(1)
			return browser.Response{Body: []byte("<h2>Preview updated</h2>"), Type: "text/html"}
		}
		if u.Path == "/favicon.ico" {
			return browser.Response{Status: 204}
		}
		if u.Path == "/error" {
			return browser.Response{Status: 503, Body: []byte("Unavailable")}
		}
		for name := range r.Headers {
			if strings.EqualFold(name, "HX-History-Restore-Request") {
				if held := holdHistory.Load(); held != nil && holdHistory.CompareAndSwap(held, nil) {
					close(held.started)
					<-held.release
				}
			}
		}
		if failNext.CompareAndSwap(1, 0) {
			return browser.Response{Status: 401, Body: []byte("Verified user required")}
		}
		pages.Add(1)
		book := books[0]
		if u.Query().Get("notebook") == books[1].ID {
			book = books[1]
		}
		v := View{LandingOrigin: "https://cloud.example.test", AccountURL: "https://auth.example.test/", User: "Fixture", Notebook: book, Notebooks: books, Writable: true, Title: "Fixture note", Key: "note.md", ETag: `"current"`, Body: "Initial body", Query: u.Query().Get("q"), Tag: u.Query().Get("tag"), HasNext: true, Total: 2, Tags: []TagCount{{Name: "ideas", Count: 1}}, Notes: []Note{{Key: "note.md", Title: "Fixture note", Tags: []string{"ideas"}}}, Connections: []store.Connection{c}, EditConnection: &c, NotebookCards: []NotebookCard{{Notebook: book}}, EditNotebook: &book, Rendered: renderMarkdown("[Other note](other.md) [Attachment](file.pdf)", book.ID, "note.md")}
		switch u.Path {
		case "/tags":
			v.TagsPage = true
		case "/note":
			v.Reading = true
		case "/edit", "/save":
			v.Editing = true
		case "/storage":
			v.StoragePage = true
		case "/notebooks":
			v.NotebooksPage = true
		}
		var out bytes.Buffer
		check(a.templates.ExecuteTemplate(&out, "page", v))
		return browser.Response{Body: out.Bytes(), Type: "text/html", Headers: map[string]string{"Cache-Control": "no-store", "Content-Security-Policy": "default-src 'none'; style-src 'self'; img-src 'self'; script-src 'self'; connect-src 'self'; form-action 'self'; base-uri 'none'; frame-ancestors 'none'"}}
	})
	defer b.Close()
	b.Navigate("https://notes.example.test/?notebook=book-one")
	b.Run(`
window.shell=document.querySelector('header');
window.confirmCount=0;
window.confirm=()=>{confirmCount++;
return false;
};
el('[aria-label=Primary] a[href^="/tags"]').click();
await wait(()=>location.pathname==='/tags'&&el('h1').textContent.includes('Find a thread'));
assert(shell===document.querySelector('header'),'header reloaded');
el('.tag-card a').click();
await wait(()=>location.search.includes('tag=ideas')&&el('.library-tools'));
el('.library-tools input[name=q]').value='term';
el('.library-tools').requestSubmit();
await wait(()=>location.search.includes('q=term'));
el('.library-tools a').click();
await wait(()=>!location.search.includes('q='));
el('.notebook-picker select').value='book-two';
el('.notebook-picker').requestSubmit();
await wait(()=>location.search.includes('book-two')&&el('#main').dataset.notebook==='book-two');
assert(el('.service-name').href.includes('book-two'),'stale header notebook');
el('.note-row h2 a').click();
await wait(()=>location.pathname==='/note');
assert(el('.document a').getAttribute('hx-boost')==='true','relative note link not boosted');
assert(!document.querySelectorAll('.document a')[1].hasAttribute('hx-boost'),'attachment link boosted');
el('.page-heading a').click();
await wait(()=>location.pathname==='/edit'&&el('#editor'));
await frame();
assert(shell===document.querySelector('header'),'editor navigation reloaded');
el('[name=content]').value='Changed draft';
el('[name=content]').dispatchEvent(new Event('input',{bubbles:true}));
await wait(()=>el('#preview-status').textContent==='Live preview'&&el('#markdown-preview').textContent.includes('Preview updated'));
assert(el('#draft-status').textContent==='Unsaved changes','new editor not bound');
const before=location.href;
el('.save-actions a').click();
await frame();
assert(location.href===before&&el('[name=content]').value==='Changed draft','canceled navigation lost draft');
const unload=new Event('beforeunload',{cancelable:true});
window.dispatchEvent(unload);
assert(unload.defaultPrevented,'full navigation guard missing');
history.back();
await wait(()=>confirmCount>=2);
await wait(()=>location.href===before);
assert(el('[name=content]').value==='Changed draft','canceled Back lost draft');
assert(confirmCount>=2,'Back did not guard draft');
`)
	b.Run(`
window.confirm=()=>true;
history.back();
await wait(()=>location.pathname==='/note'&&!document.querySelector('#editor'));
assert(shell===document.querySelector('header'),'Back reloaded shell');
history.forward();
await wait(()=>location.pathname==='/edit'&&el('#editor'));
assert(!el('#draft-status').textContent.includes('Unsaved'),'history used a draft snapshot');
el('[name=content]').value='Next draft';
el('[name=content]').dispatchEvent(new Event('input',{bubbles:true}));
await wait(()=>el('#preview-status').textContent==='Live preview');
window.confirm=()=>{window.backBlocked=true;
return false;
};
const before=location.href;
history.back();
await wait(()=>window.backBlocked);
await wait(()=>location.href===before);
assert(el('[name=content]').value==='Next draft','second Back lost draft');
window.confirm=()=>true;
el('.save-actions a').click();
await wait(()=>location.pathname==='/');
el('[aria-label=Primary] a[href="/notebooks"]').click();
await wait(()=>location.pathname==='/notebooks');
el('[aria-label=Primary] a[href="/storage"]').click();
await wait(()=>location.pathname==='/storage');
assert(shell===document.querySelector('header'),'settings reloaded shell');
history.back();
await wait(()=>location.pathname==='/notebooks');
history.forward();
await wait(()=>location.pathname==='/storage'&&el('h1').textContent==='Connections');
await frame();
assert(shell===document.querySelector('header'),'Forward reloaded shell');
`)
	// A rejected authenticated navigation preserves the current page and offers a
	// real top-level reload link rather than inserting the login/error document.
	failNext.Store(1)
	b.Run(`
const before=location.href;
el('[aria-label=Primary] a[href="/notebooks"]').click();
await wait(()=>!el('#navigation-status').hidden);
assert(location.href===before,'error pushed history');
assert(el('h1').textContent==='Connections','error replaced page');
assert(el('#navigation-status a').textContent==='Reload page','no auth recovery link');
`)
	for _, theme := range []string{"light", "dark"} {
		for _, width := range []int{320, 390, 768, 1440} {
			b.Viewport(width, 1000)
			b.Call("Emulation.setEmulatedMedia", map[string]any{"features": []map[string]string{{"name": "prefers-color-scheme", "value": theme}}})
			b.Run(`
await frame();
assert(document.documentElement.scrollWidth<=innerWidth+1,'horizontal overflow');
`)
			if (width == 390 && theme == "dark") || (width == 1440 && theme == "light") {
				data := b.Call("Page.captureScreenshot", map[string]any{"format": "png", "captureBeyondViewport": false})
				var image struct {
					Data string `json:"data"`
				}
				check(json.Unmarshal(data, &image))
				raw, err := base64.StdEncoding.DecodeString(image.Data)
				check(err)
				check(os.WriteFile("/tmp/notes-navigation-"+theme+".png", raw, 0600))
			}
		}
	}
	// Accept a delayed Back, then cancel another Back. The committed editor is
	// still visible, so cancellation must return to its entry, not the pending one.
	b.Run(`
window.confirm=()=>true;
el('.service-name').click();
await wait(()=>location.pathname==='/'&&el('.note-row'));
el('.note-row h2 a').click();
await wait(()=>location.pathname==='/note');
el('.page-heading a').click();
await wait(()=>location.pathname==='/edit'&&el('#editor'));
window.editorURL=location.href;
el('[name=content]').value='Race draft';
el('[name=content]').dispatchEvent(new Event('input',{bubbles:true}));
`)
	for _, action := range []string{"second-back", "failed-panel"} {
		held := &heldRequest{make(chan struct{}), make(chan struct{})}
		holdHistory.Store(held)
		b.Run(`
window.confirm=()=>true;
history.back();
`)
		select {
		case <-held.started:
		case <-time.After(5 * time.Second):
			t.Fatal("history request did not start")
		}
		if action == "second-back" {
			b.Run(`
window.confirm=()=>{window.raceCanceled=true;
return false;
};
history.back();
await wait(()=>window.raceCanceled);
await wait(()=>location.href===editorURL);
assert(el('[name=content]').value==='Race draft','canceled second Back discarded the visible draft');
`)
		} else {
			failNext.Store(1)
			b.Run(`
window.confirm=()=>true;
el('[aria-label=Primary] a[href="/storage"]').click();
await wait(()=>!el('#navigation-status').hidden);
await wait(()=>location.href===editorURL);
assert(el('[name=content]').value==='Race draft','failed panel request changed the visible draft');
`)
		}
		close(held.release)
		b.Run(`
await new Promise(r=>setTimeout(r,100));
assert(location.href===editorURL&&el('[name=content]').value==='Race draft','obsolete history response overwrote newer state');
`)
	}
	// Native explicit saves start a new document. Its GET editor URL and history
	// position must still work with the earlier HTMX entries and native guard.
	loader := func() string {
		var tree struct {
			FrameTree struct {
				Frame struct {
					LoaderID string `json:"loaderId"`
				} `json:"frame"`
			} `json:"frameTree"`
		}
		check(json.Unmarshal(b.Call("Page.getFrameTree", nil), &tree))
		return tree.FrameTree.Frame.LoaderID
	}
	previousLoader := loader()
	// Do not await a JavaScript promise in the document being replaced.
	b.Call("Runtime.evaluate", map[string]any{"expression": `document.getElementById('editor').requestSubmit()`, "userGesture": true})
	commitDeadline := time.Now().Add(5 * time.Second)
	for loader() == previousLoader && time.Now().Before(commitDeadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if loader() == previousLoader {
		t.Fatal("native save did not load a new document")
	}
	b.Run(`
await wait(()=>document.readyState==='complete'&&typeof window.editorURL==='undefined'&&document.querySelector('#editor'));
assert(location.pathname==='/edit','save left an unreloadable POST history URL');
window.savedURL=location.href;
window.savedShell=document.querySelector('header');
el('[name=content]').value='After native save';
el('[name=content]').dispatchEvent(new Event('input',{bubbles:true}));
`)
	dialogs := b.DialogCount()
	b.Run(`
history.back();
`)
	deadline := time.Now().Add(5 * time.Second)
	for b.DialogCount() == dialogs && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if b.DialogCount() == dialogs {
		t.Fatal("native Back did not protect the post-save draft")
	}
	b.Run(`
await frame();
assert(location.href===savedURL&&savedShell===document.querySelector('header')&&el('[name=content]').value==='After native save','native-save Back cancellation changed draft or entry');
`)
	b.AssertNoErrors()
	if previews.Load() < 2 {
		t.Fatal("editor preview did not run after repeated swaps")
	}
	t.Logf("Browser acceptance passed: %d page requests, %d live previews, 8 theme/width cases", pages.Load(), previews.Load())

}
