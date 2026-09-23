Was du tun solltest
Neues Paket holen, your-darts-paket.zip ist aktualisiert, Extension jetzt Version 0.5.0.
Server stoppen und mit dem neuen Binary starten.
Extension neu laden, dann play.autodarts.com neu laden.
Die beiden alten Matches im Admin löschen. Die lassen sich nicht retten, weil von ihnen nur der Anfangszustand gespeichert ist. „Statistiken neu berechnen“ hilft da nicht.
Falls danach noch etwas klemmt, wäre eine echte Antwort Gold wert. Ruf dazu im Browser http://localhost:8080/api/admin/matches/1/raw auf, während du eingeloggt bist. Damit sehe ich das tatsächliche Format statt nur den Code der App.

Alles ist als Commit c148df9 abgelegt, Arbeitsverzeichnis sauber, nicht gepusht.