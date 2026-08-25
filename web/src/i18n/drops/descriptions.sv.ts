// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

import type { DescriptionMap } from "../../lib/dropText";

// Swedish drop descriptions, keyed by drop id.
//
// `en` is the descriptionFingerprint (see dropText.ts) of the ENGLISH paragraph
// each translation was made from. dropDescription compares it against the
// manifest's current description and falls back to English on a mismatch, so a
// description reworded on the Go side shows the new English rather than a
// translation of behaviour that no longer exists. To refresh one: retranslate,
// then recompute the fingerprint —
//
//   h = 2166136261
//   for cp in english_text:  h = ((h ^ cp) * 16777619) & 0xFFFFFFFF
//   "%08x" % h
//
// Prose is translated; identifiers, param keys, quoted literals and API values
// ('mode', `row`, whsec_…, ORDER_OPEN) are left in English because that is what
// the user types and what the service returns. Port and step names use the
// Swedish forms from dropText.ts, so a description names what the card shows.
export const SV_DESCRIPTIONS: DescriptionMap = {
  and: {
    en: "3ec0832f",
    sv: "Skickar ut sant på Ja/Nej-utgången när ALLA inkopplade booleska indata är sanna, annars falskt (logiskt OCH). Variadisk — koppla in två eller fler Jämför-resultat (eller vilka steg som helst som ger ett Ja/Nej) och led Ja/Nej-utgången vidare till villkorsingången på ett enda Förgrening-steg. Med bara ett indata skickas det vidare oförändrat; en tom mängd ger fel.",
  },
  await_approval: {
    en: "b2065622",
    sv: "Pausa flödet tills någon godkänner. Fyll i \"Mejla de här personerna\" så mejlar Dazyflow dem godkännandelänken när flödet kommer hit, och berättar utfallet när någon har beslutat; lämnar du det tomt skickas inget mejl. Vill du avisera på något annat sätt — eller i stället — lägger du det här steget FÖRE steget som meddelar personen — du får en Godkännandelänk (utgången `pending_url`) att lägga in i meddelandet (t.ex. ntfy:s 'Länk att öppna' eller en e-posttext). Alla som har länken kan godkänna eller avslå — det går inte att rikta den till en enskild person, så skicka den bara till dem som ska besluta; flödet registrerar vem som klickade på utgången Godkännare. Personen trycker på länken för att godkänna eller avslå; först då fortsätter resten av flödet. När flödet återupptas kommer indatat Värde ut på porten Godkänt eller Avslaget beroende på beslutet (koppla var och en till sin fortsättning — inget separat Förgrening-steg behövs), tillsammans med den Godkännare som beslutade och personens Kommentar.",
  },
  base64: {
    en: "dae52265",
    sv: "Koda text till Base64 eller avkoda Base64 tillbaka till text. Använd det för att stoppa in en nyttolast i ett JSON-fält, bygga en Basic auth-rubrik (base64 av \"user:pass\") eller läsa en Base64-klump som ett API skickat tillbaka. 'mode' väljer encode (standard) eller decode; 'variant' växlar mellan vanlig Base64 och det URL-säkra alfabetet (- och _ i stället för + och /). Koppla in värdet i 'in'; resultatet kommer ut på 'out'.",
  },
  branch: {
    en: "5ed519dd",
    sv: "Dirigerar nyttolasten på porten 'in' till antingen Ja- eller Nej-utgången, utifrån Ja/Nej-värdet på ingången 'condition'. Skapa det värdet med ett Jämför-steg (koppla resultatet in i condition) — ett Ja-värde skickar nyttolasten ut på Ja-utgången; ett Nej-värde (eller ett saknat/tomt villkor) skickar den ut på Nej. Steg som är kopplade till den oanvända porten ligger vilande.",
  },
  build_csv: {
    en: "c6fc0bb4",
    sv: "Gör om rader till CSV-text — motsatsen till Läs CSV. Koppla in raderna från en databasfråga, en läsning från Sheets/Excel eller vilken omvandling som helst, och få tillbaka en enda CSV-sträng som du kan bifoga i ett mejl, skriva till en fil eller POSTa till ett API. Kolumnerna följer radernas egen kolumnordning; sätt 'columns' för att välja ut eller ändra ordning på ett urval. 'delimiter' byter avgränsare (\"\\t\"/\"tab\" för TSV, \";\" för europeiska CSV-filer) och 'header' slår rubrikraden av och på.",
  },
  builtin_store_append: {
    en: "1e9b945f",
    sv: "Spara rader i en samling — ingen databas att sätta upp och ingen anslutningssträng att klistra in. Välj ett namn på samlingen och raderna hamnar där; samlingen skapas automatiskt första gången. Varje arbetsyta har sina egna privata Samlingar, och de sparade raderna visas under Samlingar så att du kan bläddra i dem i appen. Som standard läggs rader till vid varje körning; sätt \"Unik enligt\" till en nyckelkolumn (t.ex. datum) så uppdateras en rad med samma nyckel på plats i stället för att bli en dubblett — så att en ny körning av flödet förblir idempotent.",
  },
  builtin_store_find: {
    en: "405cf2a0",
    sv: "Läs tillbaka rader ur en samling utan att skriva någon SQL. Välj samlingen och lägg till enkla villkor — som att status är lika med obetald, eller att belopp är större än 100 — i den visuella redigeraren, och de matchande raderna kommer ut. Du kan också sortera på en kolumn och begränsa hur många rader du får. Det vänliga syskonet till \"Spara rader\"; ta \"Fråga med SQL\" när du vill skriva ren SQL.",
  },
  builtin_store_query: {
    en: "406eddf9",
    sv: "Läs tillbaka rader ur en samling med en SELECT — praktiskt för att bygga en rapport av data du sparat tidigare. Använd ?-platshållare och parameterlistan för alla värden som kommer från användaren.",
  },
  chatgpt: {
    en: "76d1996e",
    sv: "Skicka en prompt och få ett svar tillbaka — sammanfatta text från ett tidigare steg, klassificera ett indata eller generera text. Koppla in texten som ska bearbetas i ingången Prompt, eller skriv en prompt direkt på steget.",
  },
  claude: {
    en: "76d1996e",
    sv: "Skicka en prompt och få ett svar tillbaka — sammanfatta text från ett tidigare steg, klassificera ett indata eller generera text. Koppla in texten som ska bearbetas i ingången Prompt, eller skriv en prompt direkt på steget.",
  },
  claude_classify: {
    en: "0c748850",
    sv: "Ge AI:n en lista med kategorier och den väljer den enda som passar bäst — dirigera supportmejl, märk upp leads, flagga skräppost. Koppla utgången Kategori till ett Förgrening-steg.",
  },
  claude_draft_reply: {
    en: "f32ad9ad",
    sv: "Mata in ett inkommande mejl eller meddelande och få tillbaka ett färdigt utkast. Välj tonläge och lägg till instruktioner. Kombinera med Vänta på godkännande innan något skickas.",
  },
  claude_extract: {
    en: "3cc059e0",
    sv: "Beskriv fälten du vill ha — som belopp, förfallodatum eller kundnamn — och AI:n läser texten och fyller i dem. Perfekt för att göra rader av fakturor, mejl och formulärsvar.",
  },
  claude_summarize: {
    en: "37e982bf",
    sv: "Mata in vilken text som helst — ett mejl, ett dokument, en rad med anteckningar — och få tillbaka en kort sammanfattning. Välj hur kort den ska vara och om du vill ha en mening, ett stycke eller punktlista.",
  },
  compare: {
    en: "e6392d92",
    sv: "Jämför två värden, A och B, och skicka ut sant eller falskt på Ja/Nej-utgången. Välj testet i en lista på vanlig svenska — är lika med, är större än, innehåller, är någon av, ligger inom intervall och mer. Koppla A och B från tidigare steg, eller skriv ett fast standardvärde direkt på steget. Kombinera Ja/Nej-utgången med ett Förgrening-steg (koppla Ja/Nej till Förgrenings villkorsingång) för att dirigera.",
  },
  compute_rows: {
    en: "d31c14b1",
    sv: "Lägg till härledda kolumner och filtrera rader med CEL-uttryck (Googles Common Expression Language). Varje uttryck ser raden som variabeln `row`: `row.first_name + ' ' + row.last_name`, `row.age >= 18`, `row.score > 90 ? 'gold' : 'bronze'`. Beräkning lägger till eller skriver över kolumner; filtret tar bort rader vars uttryck blir falskt. Det uttrycksfulla syskonet till steget Välj och byt namn på kolumner — ta det bara när statisk konfiguration inte räcker för att säga vad du menar.",
  },
  contains: {
    en: "2b868d24",
    sv: "Kontrollera om Texten på A innehåller Deltexten på B, och skicka texten ut på Ja eller Nej utifrån det. En variant av Om-steget med fast test — testet är alltid 'innehåller', så det finns inget att ställa in utom deltexten. Koppla in texten i A och skriv (eller koppla in) deltexten i B. För andra test (lika med, intervall, någon av) använd Om eller Jämför.",
  },
  cron_trigger: {
    en: "6151b18f",
    sv: "Startar flödet enligt ett återkommande schema — välj dagligen, varje vecka, varje månad eller varje timme på steget (ett eget cron-uttryck fungerar också). Utgången Tid är när det utlöstes. Utan schema körs flödet bara när du trycker på Kör.",
  },
  date: {
    en: "fb4a9f45",
    sv: "Arbeta med datum och tid: läs aktuell tid, tolka en tidsstämpel som kommit in som text, flytta den med en förskjutning, växla till en tidszon och skriv ut den i det format du vill. Koppla in ett värde i 'in' (en ISO 8601-sträng, en Unix-tidsstämpel eller vanlig datumtext) eller lämna porten okopplad för att använda aktuell tid. 'add' flyttar tiden med en förskjutning som \"3d\", \"-2h30m\" eller \"1w\"; 'tz' anger en IANA-tidszon (t.ex. \"Europe/Stockholm\") för utdatat; 'format' väljer en förinställning (iso, date, time, datetime, unix, rfc1123, kitchen) eller en egen Go-layout. Skickar ut den formaterade strängen på 'out' och de uppdelade delarna (år, månad, veckodag, …) på 'value'.",
  },
  dedupe_rows: {
    en: "9c4a95e1",
    sv: "Ta bort dubblettrader. Som standard är två rader dubbletter när varje cell stämmer; med 'by' angivet räcker det att de listade kolumnerna är lika. 'keep' väljer vilken kopia i en dubblettgrupp som får leva vidare: \"first\" (standard) eller \"last\". Ordningen från indatat bevaras för de rader som blir kvar.",
  },
  delay: {
    en: "d05d99e0",
    sv: "Pausa en valfri tid och skicka sedan det genomströmmade värdet vidare på vidarekopplingsutgången (eller skicka en styrsignal när inget värde är inkopplat, så att en ren pausning ändå startar nästa steg).",
  },
  discord_send_message: {
    en: "4cb76288",
    sv: "Lägg upp ett meddelande i en Discord-kanal via en kanalwebhook. Meddelandet ('Innehåll') kan skrivas på steget eller kopplas in från ett tidigare steg (ingången vinner över parametern). Du kan också byta visningsnamn och avatar per meddelande. Skapa webhooken i Discord under Serverinställningar → Integrationer → Webhooks och anslut den en gång på Appar-sidan — ingen bot och ingen OAuth-app behövs.",
  },
  drive_download: {
    en: "1e3dc2d9",
    sv: "Ladda ner innehållet i en fil från Google Drive till arbetsytan, färdig för nästa steg (t.ex. att mejla den som bilaga eller ladda upp den någon annanstans). Ange filens id (från steget Lista filer). Filen hamnar som standard i körningens tillfälliga utrymme; ändra namnet med 'path'. Google-dokument (Docs/Sheets/Slides) har ingen råfil, så de exporteras i stället till ett konkret format — PDF som standard, eller välj ett annat med 'Exportera som'.",
  },
  drive_list_files: {
    en: "94cb0dee",
    sv: "Lista filer i Google Drive, med möjlighet att filtrera på namn, mapp eller typ. Filer i papperskorgen utesluts som standard. Varje träff är ett objekt med id, name, mime_type, size, modified_time och web_view_link. Använd filens id i steget Ladda ner fil för att hämta innehållet.",
  },
  drive_upload: {
    en: "bb6ef092",
    sv: "Ladda upp en fil från arbetsytan till Google Drive. Koppla in en fil (t.ex. från Ladda ner fil, ett exportsteg eller en HTTP-nedladdning), eller ange en sökväg i arbetsytan. Du kan också lägga den i en särskild mapp. Returnerar den nya filens id och en delbar länk.",
  },
  elks_send_sms: {
    en: "de5e8cf8",
    sv: "Skicka ett SMS via 46elks. Mottagaren ('Till') och meddelandet ('Meddelande') kan skrivas på steget eller kopplas in från ett tidigare steg (motsvarande ingång vinner över parametern). 'Från' är antingen ett av dina 46elks-nummer (E.164, som +46700000000) eller ett alfanumeriskt avsändarnamn (upp till 11 tecken, t.ex. \"Acme\" — måste innehålla en bokstav, och mottagarna kan inte svara på det). Anslut ditt 46elks-konto en gång på Appar-sidan. Sätt 'Testkörning' för att validera utan att skicka (eller bli fakturerad).",
  },
  email_send: {
    en: "285b1497",
    sv: "Skicka e-post via din egen e-postserver (SMTP). Till, Ämne och Innehåll kan skrivas på steget eller kopplas in från ett tidigare steg (motsvarande ingång vinner över parametern) — praktiskt för utskick per mottagare eller för att mejla utdatat från ett annat steg. Bifoga filer genom att koppla filproducerande steg (t.ex. Exportera blad som PDF) till den variadiska ingången Bilagor. Ställ in e-postservern (värd, säkerhet, inloggning, avsändare) en gång på integrationssidan för E-post.",
  },
  eq: {
    en: "6d0f217c",
    sv: "Skickar ut sant på Ja/Nej-utgången när A är lika med B, annars falskt. Det minsta likhetssteget — koppla in A och B, eller skriv fasta standardvärden. Kombinera Ja/Nej-utgången med Förgrening för att dirigera. Ta Jämför i stället när du behöver rikare test (innehåller, någon av, intervall).",
  },
  excel_read: {
    en: "addbd752",
    sv: "Läs en Excel-fil (.xlsx) från arbetsytan. Första raden blir kolumnrubriker (om inte 'Första raden är rubriker' är avslaget), och varje följande rad blir ett objekt med rubrikerna som nycklar. Skriv filsökvägen på steget eller koppla in den från ett tidigare steg.",
  },
  excel_write: {
    en: "a4051ccf",
    sv: "Skriv rader till en Excel-fil (.xlsx) i arbetsytan. Koppla in en radlista i ingången Rader; kolumnerna tas från ingången Rubriker eller härleds ur radernas fält. Slå på 'Lägg till i befintligt blad' för att lägga raderna under det som redan finns i stället för att skriva om filen från början.",
  },
  expression: {
    en: "771d3f70",
    sv: "Beräkna ett enda värde med en formel — motsvarigheten på värdenivå till steget för beräknade kolumner. Skriv ett CEL-uttryck (samma formelspråk som radverktygen använder) där det inkopplade 'in'-värdet finns som `input` och aktuell tid som `now`. Använd det för att forma om ett värde mitt i flödet: plocka ut ett fält (`input.user.email`), räkna (`input * 1.25`), bygga en sträng (`\"Hej \" + input.name`), testa ett villkor (`input.status == \"paid\"`) eller omvandla en lista (`input.map(x, x.id)`). Resultatet skickas ut på 'out', med den typ formeln ger (text, boolean eller JSON). För att köra riktiga OS-kommandon eller skript använder du Shell-steget i stället — det här är en säker uttrycksberäknare i sandlåda, inte en generell körmiljö för kod.",
  },
  file_picker: {
    en: "d6b15454",
    sv: "Välj en fil i arbetsytan att starta ett flöde med. Den valda filen kommer ut på porten Fil för lässteg (Excel, CSV, …) och dess sökväg på porten Sökväg. Som standard läses filens byte INTE in i minnet — sätt inline=true för överlämning till fjärrmoduler som inte delar arbetsytan.",
  },
  file_read: {
    en: "d6db2ba5",
    sv: "Läs en fil från arbetsytan så att senare steg kan använda den. Sätt inline:true för att bädda in filens innehåll direkt, för fjärrmoduler som inte delar arbetsytan.",
  },
  file_write: {
    en: "7520b03a",
    sv: "Spara inkommande data som en fil i arbetsytan. Koppla in vad som helst i ingången Data — text, JSON eller en fil från ett annat steg — och det skrivs till den sökväg du väljer. Arbetsytans lagringsgränser respekteras.",
  },
  for_each: {
    en: "fe2be376",
    sv: "Kör Loopens innehåll — de steg som är kopplade till utgången Loopens innehåll — en gång per post i en inkommande lista. Posterna körs parallellt upp till inställningen för samtidighet. Skickar ut `results` (en post per element, i ordning) och `errors` (en lista med misslyckade rader: {row, data, error}, där row börjar på 1). Sätt fail_fast=true för att avbryta vid första felet; annars fortsätter körningen och felen kommer ut på errors-porten. Om VARJE post misslyckas misslyckas steget ändå — det är ett driftavbrott, inte en delvis lyckad körning, och ett senare steg ska inte registrera arbetet som utfört.",
  },
  fortnox_create_customer: {
    en: "e483ad73",
    sv: "Skapa en kund i ditt anslutna Fortnox-konto. Namn (obligatoriskt) och E-post kan skrivas på steget eller kopplas in från ett tidigare steg (motsvarande ingång vinner över parametern). Den nya kundens nummer kommer ut på utgången Kundnummer, färdigt att koppla in i Skapa faktura.\n\nFortnox har ingen idempotensnyckel, så det här steget gör inga automatiska omförsök — ett omförsök skulle skapa en dubblett av kunden.",
  },
  fortnox_create_invoice: {
    en: "87a46792",
    sv: "Skapa en faktura i ditt anslutna Fortnox-konto. Välj kunden (eller koppla in ingången Kundnummer från Skapa kund) och ange fakturaraderna.\n\nRaderna är Fortnox InvoiceRow-objekt — vart och ett ett objekt med Fortnox fältnamn, t.ex. {\"Description\":\"Konsultation\",\"Price\":1500,\"DeliveredQuantity\":\"2\"}. Koppla in en array i ingången Rader, eller sätt parametern 'rows'. Den skapade fakturans dokumentnummer kommer ut på 'document_number'.\n\nFortnox har ingen idempotensnyckel, så det här steget gör inga automatiska omförsök — ett omförsök skulle skapa en dubblett av fakturan.",
  },
  fortnox_list_invoices: {
    en: "798f36b9",
    sv: "Lista fakturor i ditt anslutna Fortnox-konto, med möjlighet att filtrera på status (t.ex. 'fullypaid' för ett flöde av betalda fakturor, 'unpaidoverdue' för påminnelsehantering). Nyast först, en sida per pollning.\n\nFortnox har inga webhooks, så bygg en trigger: Schema/Intervall → det här steget (filter=fullypaid) → För varje faktura → rensa dubbletter på DocumentNummer mot en 'seen'-lista i Ange hemlighet, så att varje betald faktura utlöser en gång. Bläddra vidare med parametern 'page' när 'has_more' är sant.",
  },
  gcal_create_event: {
    en: "6f8cf69f",
    sv: "Skapa en händelse i en Google-kalender. Ange en rubrik samt start- och sluttid. Använd RFC3339-tidsstämplar (2026-06-16T15:00:00Z) för en händelse med klockslag, eller enkla datum (2026-06-16) för en heldagshändelse. Deltagare är en valfri kommaseparerad lista med e-postadresser.",
  },
  gcal_list_events: {
    en: "e7fcf80d",
    sv: "Lista händelser från en Google-kalender. Avgränsa med ett tidsfönster som följer med schemat — \"tomorrow\" till \"tomorrow+1d\" för morgondagens bokningar, \"-7d\" till \"now\" för förra veckan — eller ange absoluta tidsstämplar; båda ändarna kan också kopplas in. Återkommande händelser expanderas till enskilda tillfällen och returneras i starttidsordning. Varje händelse blir ett objekt med id, summary, description, location, start/end, status och attendees.",
  },
  geo_location: {
    en: "0ef9a8a7",
    sv: "Välj en plats och skicka ut dess Koordinat (\"lat,lon\") för en väderuppslagning. Sätt en nål på OpenStreetMap-kartan direkt på kortet (sök, klicka eller dra). Eller ange en Plats — en stad eller adress — antingen skriven eller inkopplad från ett annat steg (ett formulärfält, ett meddelande): när en Plats är angiven geokodas den och ÖVERTRUMFAR kartnålen. Kartan är alltså standard, och Plats-ingången vinner när den finns. Använder OpenStreetMap — inget konto och ingen nyckel.",
  },
  geo_reverse: {
    en: "0abd795e",
    sv: "Motsatsen till Plats: sätt namn på en punkt på kartan. Sätt en nål på OpenStreetMap-kartan direkt på kortet — eller koppla in en Koordinat (\"lat,lon\") från ett annat steg (ett Plats-val, en enhets GPS) för att övertrumfa den — och du får tillbaka det mänskliga platsnamnet (\"Stockholm, Södermanland, Sverige\") plus den strukturerade Adressen. Praktiskt för att sätta etikett på en avisering: 'Regn väntas nära <plats>'. Använder OpenStreetMap — inget konto och ingen nyckel.",
  },
  git_checkout: {
    en: "9b718efd",
    sv: "Hämta en kopia av ett git-repo till arbetsytan, med möjlighet att växla till en särskild gren, tagg eller commit. Filerna blir tillgängliga för stegen efter det här — användbart för att granska källkod, hämta mallar eller lägga upp filer för bearbetning.",
  },
  git_diff: {
    en: "538e043a",
    sv: "Visa vad som ändrats mellan två punkter i historiken i ett utcheckat repo, som en unified diff (patchtext). Som standard visas ändringarna i den senaste commiten (HEAD~1..HEAD), men du kan jämföra vilka två grenar, taggar eller commits som helst.",
  },
  git_log: {
    en: "17abd3bd",
    sv: "Lista de senaste commitarna i ett utcheckat repo — var och en med sin SHA, avsändare, tid och sammanfattningsrad. Användbart för att visa releasenoteringar, tillskriva ändringar eller bygga rapporter av typen 'det här landade i dag'.",
  },
  github_add_comment: {
    en: "5aff2050",
    sv: "Kommentera ett GitHub-ärende eller en PR (de delar nummerserie). Kommentaren kan skrivas på steget eller kopplas in från ett annat steg (ingången vinner över det skrivna värdet); Markdown fungerar. Skickar ut en länk till den publicerade kommentaren.",
  },
  github_create_issue: {
    en: "1c1dabdd",
    sv: "Öppna ett nytt ärende i ett GitHub-repo. Rubrik och Innehåll kan skrivas på steget eller kopplas in från ett annat steg (motsvarande ingång vinner över det skrivna värdet); innehållet stöder Markdown. Skickar ut det nya ärendets länk och nummer så att ett senare steg kan lägga upp det någonstans eller kommentera på det.",
  },
  github_list_issues: {
    en: "5e94e7c5",
    sv: "Hämta en lista med ärenden från ett GitHub-repo, filtrerad på öppet/stängt, etiketter eller tilldelad person. Pull requests utesluts som standard (GitHubs API räknar dem som ärenden) — slå på 'Inkludera pull requests' för att behålla dem. Resultaten hämtas över flera sidor upp till 'Max antal träffar'. Passar med en pollningstrigger för flöden av typen 'gör något vid nytt ärende': filtrera på 'Uppdaterad efter' (senast sedda tidsstämpel) och bearbeta det som kommer tillbaka.",
  },
  github_on_new_pr: {
    en: "4d41497a",
    sv: "Startar flödet när en pull request öppnas i det anslutna repot. Återöppningar och nya pushar utlöser det inte — det är just ögonblicket 'ny PR'. Skickar ut PR:ens nummer, rubrik, beskrivning, avsändare, käll- och målgren samt en länk till den.",
  },
  github_on_push: {
    en: "f48ba20a",
    sv: "Startar flödet när commits pushas till det anslutna repot. Skickar ut grenen (ref), commit-SHA före och efter, listan med commits, repot och vem som pushade. Vanliga användningar: lägg upp en driftsättningsavisering när commits landar på main, eller starta en CI-liknande kedja.",
  },
  gmail_get_attachments: {
    en: "5589fb62",
    sv: "Ta filerna som är bifogade till ett mejl och spara dem, färdiga att lämna vidare till ett steg som lägger dem någonstans — Google Drive · Ladda upp fil, Skriv fil, eller ett eget mejl. Koppla Matchande mejl från Sök mejl in i en För varje och lägg det här steget i Loopens innehåll med E-post = radens id. Använd 'Bara dessa typer' för att bara ta PDF:erna och strunta i signaturbilderna. Utgången Första filen är den du kopplar när varje mejl bär ett enda dokument; listan Filer bär alla.",
  },
  gmail_get_message: {
    en: "7fd53d76",
    sv: "Läs ett mejl som lättlästa värden för Datum / Avsändare / Ämne / Innehåll. Koppla Sök mejls utgång Matchande mejl direkt till Meddelande-ID för att läsa DEN FÖRSTA träffen — eller, för att läsa varje träff, koppla Matchande mejl till ett För varje och lägg det här steget i loopens innehåll med Meddelande-ID = radens id.",
  },
  gmail_get_thread: {
    en: "00e45a5f",
    sv: "Läs alla meddelanden i en konversation, äldst först, och — det användbara — få veta om någon har svarat än. Besvarad är Nej så länge det nyaste meddelandet i tråden fortfarande är ditt eget, vilket är precis det \"de har inte hört av sig\"-test ett uppföljningsflöde behöver. Koppla Matchande mejl från Sök mejl in i en För varje och lägg det här steget i Loopens innehåll med Konversation = radens threadId. Sammanfattning är en rad per konversation (ämne, vem som skrev sist, när, hur många meddelanden, besvarad) — samla dem med Samla resultat från loopen för att få en tabell över vad som är obesvarat.",
  },
  gmail_search_messages: {
    en: "9977ee19",
    sv: "Hitta mejl i den anslutna brevlådan. Sökningen fungerar precis som Gmails eget sökfält (t.ex. 'from:chefen@foretaget.se is:unread' eller 'newer_than:1d'). Varje träff kommer ut som ett riktigt mejl — datum, avsändare, ämne och innehåll — färdigt att logga till ett kalkylblad, loopa över med För varje, eller koppla in i Gmail · Läs mejl för att ta det senaste.",
  },
  gmail_send_email: {
    en: "3f1ec8d6",
    sv: "Skicka ett mejl från den anslutna brevlådan. Till, Ämne och Innehåll kan skrivas som parametrar eller kopplas in från ett tidigare steg (motsvarande ingång vinner över parametern) — praktiskt för utskick per mottagare. Bifoga filer genom att koppla filproducerande steg (t.ex. Exportera blad som PDF) till den variadiska ingången Bilagor.",
  },
  google_form_trigger: {
    en: "399df000",
    sv: "Bevakar ett Google-formulär och utlöses när nya svar kommer in (varje svar exakt en gång). `responses` är en lista med objekt där frågans rubrik är nyckeln — koppla den direkt till en tillägg i Sheets. Varje svar bär också `email` (svarandens adress) när formuläret samlar in e-postadresser, så att du kan svara dem. När en kontroll inte hittar något nytt hoppas resten av flödet över. Publicera flödet så att det körs automatiskt enligt schemat nedan — att trycka Kör gör bara en kontroll, för test.",
  },
  gpt_classify: {
    en: "0c748850",
    sv: "Ge AI:n en lista med kategorier och den väljer den enda som passar bäst — dirigera supportmejl, märk upp leads, flagga skräppost. Koppla utgången Kategori till ett Förgrening-steg.",
  },
  gpt_draft_reply: {
    en: "f32ad9ad",
    sv: "Mata in ett inkommande mejl eller meddelande och få tillbaka ett färdigt utkast. Välj tonläge och lägg till instruktioner. Kombinera med Vänta på godkännande innan något skickas.",
  },
  gpt_extract: {
    en: "3cc059e0",
    sv: "Beskriv fälten du vill ha — som belopp, förfallodatum eller kundnamn — och AI:n läser texten och fyller i dem. Perfekt för att göra rader av fakturor, mejl och formulärsvar.",
  },
  gpt_summarize: {
    en: "37e982bf",
    sv: "Mata in vilken text som helst — ett mejl, ett dokument, en rad med anteckningar — och få tillbaka en kort sammanfattning. Välj hur kort den ska vara och om du vill ha en mening, ett stycke eller punktlista.",
  },
  group_aggregate: {
    en: "f712cd9b",
    sv: "Gruppera rader på N kolumner och skicka ut en rad per grupp med aggregerade värden. Parametern `by` är listan med grupperingskolumner. Parametern `aggregate` kopplar varje utgående kolumn till {op, column} — stödda operationer: count (ingen kolumn behövs), sum, avg, min, max, first, last, collect (samlar värden i en lista). Numeriska operationer omvandlar strängar (\"30\") och heltal/decimaltal, så blandade indata från Excel/JSON fungerar utan att du behöver typomvandla först. min/max faller tillbaka på jämförelse i bokstavsordning när värdena inte alla är numeriska. Grupperna skickas ut i den ordning de först dök upp. by:[] = en enda grupp som täcker alla rader — användbart för totalsummor över hela indatat.",
  },
  gt: {
    en: "f148ae3c",
    sv: "Skickar ut sant på Ja/Nej-utgången när det numeriska A är strikt större än B, annars falskt. Båda operanderna måste vara tal. Kombinera Ja/Nej-utgången med Förgrening för att dirigera.",
  },
  gte: {
    en: "30ef2dc5",
    sv: "Skickar ut sant på Ja/Nej-utgången när det numeriska A är större än eller lika med B, annars falskt. Kombinera Ja/Nej-utgången med Förgrening för att dirigera.",
  },
  hash: {
    en: "5036884e",
    sv: "Beräkna en hash av indatat — en vanlig summa för en kontrollsumma eller dubblettnyckel, eller en nycklad HMAC för att verifiera (eller signera) en webhook. 'algo' väljer sha256 (standard), sha512, sha1 eller md5. Sätt 'key' för att gå från en vanlig hash till HMAC med den hemligheten (koppla in en ${secret.…}). 'encoding' skriver ut summan som hex (standard) eller base64. sha1/md5 finns bara för kompatibilitet och kontrollsummor — använd sha256 eller starkare för allt som är säkerhetskritiskt.",
  },
  homeassistant_call_service: {
    en: "f7207ac5",
    sv: "Säg åt Home Assistant att göra något: släck eller tänd en lampa eller strömbrytare, kör ett skript, aktivera en scen, ställ en termostat. Välj tjänsten (som light.turn_on) och enheten den gäller; båda kan skrivas på steget eller kopplas in från ett annat steg. Extra inställningar (ljusstyrka, temperatur, …) läggs i 'Tjänstdata'.",
  },
  homeassistant_get_state: {
    en: "687f4955",
    sv: "Slå upp hur något står just nu — är dörren öppen, vad är temperaturen, är lampan tänd. Ge den en enhet (som sensor.kitchen_temperature) och den returnerar aktuell Status plus alla Attribut, färdigt att koppla in i en avisering, ett villkor eller ett kalkylblad. Enheten kan skrivas på steget eller kopplas in från ett annat steg.",
  },
  homeassistant_state_changed: {
    en: "c7d64e41",
    sv: "Bevakar en enhet och startar flödet när dess status ändras — ytterdörren öppnas, temperaturen passerar ett värde, en lampa tänds. Skickar ut den nya Statusen, den Tidigare statusen och enhetens Attribut. När en kontroll inte hittar någon ändring hoppas resten av flödet över. Publicera flödet så att det körs automatiskt enligt schemat nedan; att trycka Kör gör bara en kontroll (och registrerar aktuell status utan att utlösa, så att nästa riktiga ändring utlöser rent).",
  },
  http_download: {
    en: "d652e2d3",
    sv: "Ladda ner en fil från en webbadress och spara den i arbetsytan. Nedladdningen strömmas till disk, så filer som är för stora för minnet går bra; arbetsytans lagringsgränser respekteras medan den skrivs. Använd en scratch://-sökväg för mellanfiler som ska städas bort när körningen slutar. Adresser i privata nät är blockerade som standard.",
  },
  http_request: {
    en: "9a4c5ce3",
    sv: "Anropa vilken webbadress (API) som helst — GET, POST, PUT, PATCH eller DELETE. Användbart när tjänsten du vill prata med inte har ett eget steg här än. Svaret, statuskoden och rubrikerna kommer ut på egna portar, så att ett Förgrening-steg kan testa statusen direkt. Adresser i privata nät är blockerade som standard för att förhindra oavsiktliga interna anrop.",
  },
  http_upload: {
    en: "36c57efe",
    sv: "Skicka en fil från arbetsytan till en webbadress, strömmad från disk. Som standard skickas filens byte direkt — vilket är vad uppladdningslänkar från S3/GCS/Azure förväntar sig; slå på 'Skicka som formuläruppladdning' för tjänster som vill ha filen som en formulärbilaga. Läser även scratch://-sökvägar. Adresser i privata nät är blockerade som standard.",
  },
  if: {
    en: "032948c3",
    sv: "Testa värdet på A och skicka det ut på Ja eller Nej i ett och samma steg. Välj testet i en lista på vanlig svenska — är lika med, innehåller, är större än, är någon av, ligger inom intervall och mer. A är både värdet som testas och nyttolasten som går vidare: den lämnar steget via Ja när testet stämmer, via Nej när det inte gör det. Koppla in B från ett tidigare steg eller skriv ett fast standardvärde. Det här är Jämför + Förgrening sammanslaget för det vanliga fallet med ett enda villkor; ta Jämför → Förgrening när du behöver dirigera en annan nyttolast än den du testar, eller kombinera villkor med Och/Eller/Inte.",
  },
  join_rows: {
    en: "e9b3181a",
    sv: "SQL JOIN mellan två radströmmar. Parametern `on` kopplar vänstra kolumner till högra ({\"id\": \"user_id\"}). `kind` väljer inner / left / right / outer / anti (anti = bara de vänstra raderna som saknar matchning på högersidan, med enbart sina egna kolumner — frågan \"vilka av de här har jag inte behandlat än?\"). När samma nyckel matchar flera högra rader blir utdatat en kartesisk produkt inom den gruppen (som i vanlig SQL). Högra kolumner som inte är nycklar och krockar med vänstra kolumnnamn får ett suffix (standard \"_right\", ändras med `right_suffix`). Den högra sidans nyckelkolumner tas bort ur utdatat eftersom de per definition är lika med den vänstras.",
  },
  json: {
    en: "067c3bd7",
    sv: "Skickar ut ett fast JSON-värde, avkodat. Skriv en JSON-array eller ett JSON-objekt och det tolkas en gång vid körning och skickar ut det riktiga värdet (inte en sträng) på porten 'out' — så att det kan kopplas direkt till portar som vill ha strukturerad JSON, som Blocks i ett Slack-meddelande. Till skillnad från Text, som skickar ut sitt innehåll som en vanlig sträng.",
  },
  klarna_capture_order: {
    en: "a0077dde",
    sv: "Debitera en Klarna-order när du levererat den — steget som flyttar pengar efter att ett köp har reserverats. Ange order-id (skrivet eller inkopplat från ett tidigare steg); lämna Belopp tomt för att debitera hela det återstående reserverade beloppet, eller ange det i valutans minsta enhet (öre/cent) för en delvis debitering. En kort Beskrivning visas på kundens Klarna-kontoutdrag.\n\nDen nya debiteringens id kommer ut på 'capture_id'. Att debitera en order tar pengar från kunden, så det här steget körs en gång och görs aldrig om automatiskt — Dazyflow debiterar inte samma order två gånger. Anslut ditt Klarna-konto en gång på Appar-sidan.",
  },
  klarna_get_order: {
    en: "be171703",
    sv: "Hämta en order från ditt anslutna Klarna-konto med dess order-id (från Klarnas kassa-återkoppling eller Merchant Portal). Order-id kan skrivas på steget eller kopplas in från ett tidigare steg (ingången Order-ID vinner över parametern).\n\nUt kommer orderns 'status' (ORDER_OPEN, PART_CAPTURED, CAPTURED, CANCELLED, EXPIRED, CLOSED), 'order_amount', 'captured_amount' och 'remaining_authorized_amount' (alla i valutans minsta enhet — öre/cent), 'currency', samt hela ordern som JSON på utgången Order. Det här är en läsning — säker att göra om. Anslut ditt Klarna-konto en gång på Appar-sidan.",
  },
  klarna_refund_order: {
    en: "8e14f5a5",
    sv: "Återbetala en debiterad Klarna-order med dess order-id (skrivet eller inkopplat från ett tidigare steg). Lämna Belopp tomt för att återbetala hela det återstående återbetalningsbara beloppet (debiterat minus redan återbetalat), eller ange det i valutans minsta enhet (öre/cent) för en delvis återbetalning. En kort Beskrivning visas på kundens Klarna-kontoutdrag. Lägg ett godkännandesteg före det här för det klassiska flödet 'godkänn i Slack → återbetala'.\n\nDen nya återbetalningens id kommer ut på 'refund_id'. En återbetalning betalar tillbaka pengar, så det här steget körs en gång och görs aldrig om automatiskt — Dazyflow återbetalar inte samma order två gånger. Anslut ditt Klarna-konto en gång på Appar-sidan.",
  },
  lt: {
    en: "2c87b245",
    sv: "Skickar ut sant på Ja/Nej-utgången när det numeriska A är strikt mindre än B, annars falskt. Båda operanderna måste vara tal. Kombinera Ja/Nej-utgången med Förgrening för att dirigera.",
  },
  lte: {
    en: "696fd8aa",
    sv: "Skickar ut sant på Ja/Nej-utgången när det numeriska A är mindre än eller lika med B, annars falskt. Kombinera Ja/Nej-utgången med Förgrening för att dirigera.",
  },
  map_rows: {
    en: "ae0d4c05",
    sv: "Forma om en radström mellan två steg: välj ut eller ta bort kolumner, byt namn, fyll i saknade värden, filtrera rader på likhet / olikhet / medlemskap. Alla operationer utgår från kolumnnamnen i INDATAT; namnbyten sker sist, så utdatat använder de nya namnen. Ren konfiguration, inget uttrycksspråk — täcker de flesta fallen av 'mina Excel-kolumner matchar inte mitt databasschema'.",
  },
  merge: {
    en: "4c7380ca",
    sv: "Vänta in N inkommande indata och skicka ut dem som en enda lista på ut-porten. Användbart som synkroniseringspunkt i parallella grenar.",
  },
  mqtt_publish: {
    en: "9d9942d9",
    sv: "Publicera ett meddelande till ett ämne (topic) på en MQTT-mäklare. Ämnet och nyttolasten kan skrivas på steget eller kopplas in från ett tidigare steg (motsvarande ingång vinner över parametern). Mäklaren anges som en tcp:// - eller ssl://-adress (bara värd:port blir tcp://…:1883). Mäklarens adress och eventuellt användarnamn/lösenord ställs in en gång som MQTT-anslutningen på Appar-sidan. Mäklare i privata nät är blockerade om inte operatören tillåter utgående trafik dit.",
  },
  mysql_insert_rows: {
    en: "725ac730",
    sv: "Infoga rader i en MySQL- eller MariaDB-tabell. Tar rader från Sheets, Excel eller vilket omvandlingssteg som helst — formen är utbytbar mellan de stegen.",
  },
  mysql_query: {
    en: "3f603bd2",
    sv: "Kör en SELECT mot din MySQL- eller MariaDB-databas och få tillbaka rader. Använd ?-platshållare i SQL:en och skicka värdena genom params-arrayen, så att data från användare escapas säkert. Sätt en radgräns för att hålla stora resultatmängder i schack.",
  },
  mysql_upsert_rows: {
    en: "bd54d453",
    sv: "Infoga eller uppdatera (upsert) rader i en MySQL- eller MariaDB-tabell. Ange konfliktkolumnerna — MySQL matchar befintliga rader på dem och uppdaterar dem på plats, medan nya rader infogas. Rapporterar antal infogade och uppdaterade separat, så att senare aviseringar kan säga 'X nya + Y uppdaterade'.",
  },
  neq: {
    en: "949cec57",
    sv: "Skickar ut sant på Ja/Nej-utgången när A inte är lika med B, annars falskt. Kombinera Ja/Nej-utgången med Förgrening för att dirigera.",
  },
  not: {
    en: "cc67a377",
    sv: "Skickar ut den logiska negationen av det inkopplade Ja/Nej-indatat på Ja/Nej-utgången — sant blir falskt, falskt blir sant. Koppla in ett Jämför-resultat (eller vilket steg som helst som ger ett Ja/Nej) i ingången; led Ja/Nej-utgången till villkorsingången på ett Förgrening-steg för att byta vilken port en nyttolast tar utan att koppla om förgreningen.",
  },
  notion_create_page: {
    en: "1cdb59df",
    sv: "Lägg till en sida i Notion. Skriv en Rubrik och ett valfritt Sidinnehåll (tomma rader startar nya stycken) och välj sedan var den ska hamna: i en databas (sidan blir en rad) eller under en föräldersida (den blir en undersida). Rubrik och Sidinnehåll kan också kopplas in från ett tidigare steg — en inkoppling vinner över det skrivna värdet. Extra databaskolumner (Status, datum, taggar …) läggs i det avancerade fältet 'properties' som rå Notion-JSON.",
  },
  notion_query_database: {
    en: "4ca5a4ae",
    sv: "Läs rader från en Notion-databas. Varje matchande sida blir en enkel post av sina kolumner (Namn, Status, datum, taggar …) som vanliga värden, plus sitt id och url — färdigt att logga till ett kalkylblad, loopa över med För varje, eller koppla in i Notion · Skapa sida. Begränsa eller sortera resultaten med rå Notion-JSON i de avancerade fälten Filter och Sortering.",
  },
  nshift_create_shipment: {
    en: "6578632b",
    sv: "Skapa (boka) en försändelse hos en transportör via nShift. Ge den uppgifterna om försändelsen — avsändare, mottagare, kolli och transportörens tjänst — på ingången Försändelse, oftast byggd av ett tidigare steg för varje order (du kan också skriva den på steget).\n\nUt kommer den nya försändelsens 'shipment_id', kollinas 'tracking_numbers' (kommaseparerade) och hela den skapade försändelsen som JSON på utgången Försändelse. Att boka en försändelse kostar pengar, så det här steget körs en gång och görs aldrig om automatiskt — Dazyflow bokar inte samma sändning två gånger. Anslut ditt nShift-konto en gång på Appar-sidan; lämna anslutningen på 'integration' för att testa utan att boka en verklig sändning.",
  },
  nshift_delete_shipment: {
    en: "31449403",
    sv: "Ta bort en försändelse från nShift med dess id — sättet att avboka en sändning du bokat av misstag. nShift tillåter bara att ta bort en försändelse som inte har skrivits ut/bekräftats; en utskriven nekas och orsaken visas. Id:t kan skrivas på steget eller kopplas in från ett tidigare steg (ingången Försändelse-ID vinner över parametern).\n\nUt kommer 'deleted' (true) vid lyckat borttagande. Borttagningen går inte att ångra, så det här steget körs en gång och görs aldrig om automatiskt. Anslut ditt nShift-konto en gång på Appar-sidan.",
  },
  nshift_get_shipment: {
    en: "938a85ef",
    sv: "Hämta en försändelse från nShift med dess försändelse-id (som du fick när den skapades, eller från nShift Delivery). Id:t kan skrivas på steget eller kopplas in från ett tidigare steg (ingången Försändelse-ID vinner över parametern).\n\nUt kommer kollinas 'tracking_numbers' (kommaseparerade) och hela försändelsen som JSON på utgången Försändelse — kombinera det här med en pollningstrigger för att reagera på ändrad leveransstatus. Det här är en läsning — säker att göra om. Anslut ditt nShift-konto en gång på Appar-sidan.",
  },
  ntfy: {
    en: "07cd61a8",
    sv: "Skicka en push-notis till ett ntfy-ämne — prenumerera på samma ämne i den kostnadsfria ntfy-appen för att få den i telefonen. Meddelandet kan skrivas på steget eller kopplas in från ett annat steg; rubrik, prioritet, taggar och en länk att trycka på är valfria. Använder som standard den publika servern ntfy.sh; för en egen ntfy-server anger du dess Server-URL (och en token för skyddade ämnen) en gång via ntfy-anslutningen (Appar-sidan eller verktyget configure_connection) — flödena bär då bara ämne och meddelande per notis.",
  },
  number: {
    en: "21c290ba",
    sv: "Skickar ut ett fast numeriskt värde. Senare steg ser det som ett JSON-tal på porten 'out' — koppla det till en jämförelse (Inom intervall, Jämför), en operator eller ett numeriskt indata som varaktigheten i ett Fördröjning-steg.",
  },
  openmeteo_current: {
    en: "671a4553",
    sv: "Slå upp vädret just nu för vilken punkt som helst på kartan med Open-Meteos kostnadsfria prognos — inget konto och ingen API-nyckel för privat, icke-kommersiell användning. Ge den en koordinat — skriv Latitud och Longitud, eller koppla in ett \"lat,lon\"-värde från ett Plats-/geokodningssteg — och du får tillbaka en Sammanfattning på en rad plus Temperaturen och ett Väderläge-ord (Clear, Rain, Snow …) som du kan förgrena på, samt hela svaret som JSON. För kommersiell användning lägger du in din Open-Meteo API-nyckel på integrationssidan, varpå den växlar till den betalda ändpunkten.",
  },
  openmeteo_forecast: {
    en: "d1b227ad",
    sv: "Se upp till 16 dagar framåt för vilken punkt som helst på kartan med Open-Meteos kostnadsfria prognos — inget konto och ingen API-nyckel för privat, icke-kommersiell användning. Ge den en koordinat — skriv Latitud och Longitud, eller koppla in ett \"lat,lon\"-värde från ett Plats-/geokodningssteg — och välj antal dagar. Du får en läsbar Sammanfattning dag för dag plus arrayen Per dygn som JSON (min-/maxtemperatur, väderläge och regnrisk per dag). För kommersiell användning lägger du in din Open-Meteo API-nyckel på integrationssidan, varpå den växlar till den betalda ändpunkten.",
  },
  or: {
    en: "d589aa81",
    sv: "Skickar ut sant på Ja/Nej-utgången när NÅGOT av de inkopplade booleska indata är sant, annars falskt (logiskt ELLER). Variadisk — koppla in två eller fler Jämför-resultat (eller vilka steg som helst som ger ett Ja/Nej) och led Ja/Nej-utgången vidare till villkorsingången på ett enda Förgrening-steg. Med bara ett indata skickas det vidare oförändrat; en tom mängd ger fel.",
  },
  parse_csv: {
    en: "ed343c3a",
    sv: "Gör om CSV-text till rader. Mata in ett HTTP-svar, innehållet i en nedladdad fil eller vilken kommaseparerad text som helst, och den tolkas till den vanliga formen med rader + rubriker som Sheets, Excel, Postgres och omvandlingsfamiljen använder. Som standard är första raden rubrikrad och namnger kolumnerna; sätt 'header' till false för data utan rubriker (kolumnerna blir col1, col2, …). 'delimiter' byter avgränsare — använd \"\\t\" eller \"tab\" för tabbseparerade värden, \";\" för europeiska CSV-filer. Rader som är kortare än rubriken fylls ut med tomma strängar; längre rader behåller sina extra celler under de utfyllda namnen.",
  },
  parse_json: {
    en: "eb683527",
    sv: "Gör om JSON-text till rader. Mata in textutdatat från ett AI-steg eller ett HTTP-svar och JSON:en tolkas till den vanliga formen med rader + rubriker som Sheets, Excel, Postgres och omvandlingsfamiljen använder. En JSON-array med objekt blir en rad per objekt; ett enskilt objekt blir en rad. Tål de omslag modeller lägger till: inledande och avslutande prosa och Markdown-kodstaket (```json … ```) tas bort före tolkningen. Använd 'path' för att nå en array som ligger inne i ett hölje (t.ex. \"data.items\").",
  },
  parse_xml: {
    en: "bbb18f23",
    sv: "Gör om XML-text till rader eller ett strukturerat värde. Mata in ett HTTP-svar, en nedladdad fil eller en RSS-/SOAP-nyttolast och den tolkas till formen med rader + rubriker som omvandlingsfamiljen, Sheets och databasstegen använder. Omvandlingen: ett elements attribut blir nycklar med @ först (id=\"7\" → \"@id\":\"7\"), underelement blir nycklar (upprepade underelement blir en lista) och textinnehållet är elementets värde (eller \"#text\" vid sidan av attribut/underelement). Dokumentets rotelement skalas bort, så 'path' är relativ till dess barn — peka den på det upprepade elementet för att få en rad per stycke (t.ex. \"channel.item\" för RSS). Namnrymder kortas till sitt lokala namn. Alla värden är text, som i CSV.",
  },
  phone: {
    en: "e83b5bbd",
    sv: "Håll ett telefonnummer — skriv det direkt eller koppla in en sträng i ingången 'phone' — och skicka ut det som rent E.164 (+46701234567) på 'out', men först efter en kontroll att det är ett verkligt, ringbart nummer. Lokala format förstås: med standardregion SE blir \"070-123 45 67\" till \"+46701234567\". Ett nummer som inte är giltigt får steget att misslyckas direkt, i stället för att dyka upp som ett kryptiskt fel när ett senare SMS-steg nekar det. Numret delas också upp så att du kan agera på delarna utan strängfiffel: 'country' (SE), 'national' (701234567) och 'type' (mobile / fixed_line / …). Koppla 'out' direkt in i SMS-stegen för 46elks eller Twilio.",
  },
  poll_trigger: {
    en: "e40667de",
    sv: "Startar flödet gång på gång i ett fast tempo — var några minuter, timmar eller dagar. Utgången Tid är när det utlöstes. Utan angivet intervall körs flödet bara när du trycker på Kör.",
  },
  postgres_insert_rows: {
    en: "6c23ea2e",
    sv: "Infoga rader i en Postgres-tabell. Tar rader från Sheets, Excel eller vilket omvandlingssteg som helst — formen är utbytbar mellan de stegen, så ingen extra mappning behövs.",
  },
  postgres_query: {
    en: "ea077c00",
    sv: "Kör en SELECT mot din Postgres-databas och få tillbaka rader. Använd platshållarna $1, $2 i SQL:en och skicka värdena genom params-arrayen, så att data från användare escapas säkert. Sätt en radgräns för att hålla stora resultatmängder i schack.",
  },
  postgres_upsert_rows: {
    en: "8ee001e8",
    sv: "Infoga eller uppdatera (upsert) rader i en Postgres-tabell. Ange konfliktkolumnerna — Postgres matchar befintliga rader på dem och uppdaterar dem på plats, medan nya rader infogas. Välj vilka kolumner som uppdateras vid en träff om du vill bevara några befintliga värden.",
  },
  regex: {
    en: "bc6c9730",
    sv: "Kör ett reguljärt uttryck över text — koppla in texten, eller skriv den på steget (så att du inuti en För varje kan läsa ${item.description} utan något föregående steg). 'mode' väljer vad som ska göras: extract plockar ut varje träff (med grupper som kolumner — den första träffen kommer också ut på 'out'), replace ersätter träffar (använd $1 eller ${name} i ersättningen), split delar texten på mönstret till en lista, och match testar om mönstret finns (ett Ja/Nej för ett Förgrening-steg). Mönstren använder RE2-syntax; lägg till inbyggda flaggor som (?i) för skiftlägesokänslighet. Namngivna grupper (?P<name>…) blir namngivna kolumner; onamngivna blir 1, 2, … och hela träffen är 'match'.",
  },
  render_table: {
    en: "b551f318",
    sv: "Gör en radlista direkt till en färdig HTML-tabell — kolumnnamnen blir rubrikrad och varje rad blir en tabellrad. Till skillnad från Gör text finns ingen mall att skriva och inga kolumnnamn att skriva in: rubrikerna kommer från de kolumner datat faktiskt har, så de kan inte glida ifrån källan (inget \"no such key\" vid körning). Koppla in en radlista i `rows` och utgången `html` till ett meddelandesteg — t.ex. Innehåll i ett Skicka e-post-steg. Med noll rader skickas `empty` ut (standard \"\"), så ett tomt resultat ger den reserv du valt i stället för en tom tabell.",
  },
  render_template: {
    en: "a3070120",
    sv: "Rendera en HTML-mall med kopplingsfält till en enda HTML-sträng — din egen profilerade layout, fylld med dynamiskt innehåll, färdig att koppla till Innehåll i ett mejl. Mallen använder Go:s html/template-syntax: {{.name}} hämtar ett fält ur datat, {{range .items}}…{{end}} loopar, {{if .vip}}…{{end}} förgrenar. Skriv mallen på steget, eller koppla in den från en fil i arbetsytan (ett Läs fil-steg på en .html). Koppla in kopplingsdatat (ett JSON-objekt, t.ex. en rad från ett kalkylblad eller en webhook-kropp) i ingången 'data' — mallen ser det som roten, så {{.customer}} läser data.customer. Värden HTML-escapas automatiskt, så ett kundnamn som innehåller <script> kan inte förstöra din markup. Den renderade HTML:en går direkt in i Innehåll i ett Skicka e-post-steg (sätt det stegets format till HTML).",
  },
  render_text: {
    en: "f4ee3a59",
    sv: "Slå samman en lista med rader till en enda textsträng: rendera en rad per post (ett CEL-uttryck eller en enskild kolumn) och foga sedan ihop raderna med en avgränsare. Det här är bryggan mellan de tabellsteg och de meddelandesteg — Skicka Slack-meddelande, Skicka e-post och Skapa GitHub-ärende vill alla ha en enda text i sitt Innehåll/Text-fält, inte en lista med rader. Koppla det här stegets textutgång till det fältet. Med noll rader skickas `empty` ut (standard \"\"), så du kan skriva \"Inga nya ordrar i dag.\" i stället för att misslyckas på ett tomt meddelande.",
  },
  roaring_company_overview: {
    en: "a5d2b75c",
    sv: "Slå upp ett företag i Roaring med dess organisationsnummer — berikningssteget som gör ett ensamt orgnummer (t.ex. från en order eller ett formulär) till strukturerad företagsdata: registrerat namn, status, adress och skatteuppgifter. Orgnumret kan skrivas på steget eller kopplas in från ett tidigare steg (ingången Organisationsnummer vinner över parametern). Utgår från Sverige ('se'); sätt 'country' för en annan nordisk marknad som Roaring täcker.\n\nUt kommer 'name' och 'status' som text samt hela Roaring-posten på utgången Företag. Det här är en läsning — säker att göra om. Anslut ditt Roaring-konto en gång på Appar-sidan (Consumer Key + Secret).",
  },
  roaring_company_search: {
    en: "8ebf1b3f",
    sv: "Sök företag på namn i Roaring — uppslagssteget före berikningen: gör ett skrivet eller inkopplat företagsnamn till möjliga träffar (var och en med sitt organisationsnummer), som du sedan skickar till Företagsöversikt. Söktexten kan skrivas på steget eller kopplas in från ett tidigare steg (ingången Fråga vinner över parametern). Utgår från Sverige ('se').\n\nUt kommer antalet träffar som text samt hela Roarings sökresultat på utgången Resultat (loopa det med ett För varje för att berika varje träff). Det här är en läsning — säker att göra om. Anslut ditt Roaring-konto en gång på Appar-sidan (Consumer Key + Secret).",
  },
  route_rows: {
    en: "d1e514f6",
    sv: "Delning i N vägar. Parametern `routes` är en ordnad lista med poster av typen {slot, filter} — för varje rad vinner det FÖRSTA filtret som blir sant, och raden går ut på den utgången. Rader som inte matchar någon väg hamnar på `default`. Använd det för att dela en radström i kedjor per kategori (t.ex. dirigera SE-/NO-/UK-ordrar till olika efterföljande bearbetning). Namnen på utgångarna är fasta (rows_1..rows_8 + default) i V1; semantiska namn via variadiska portar är en framtida förbättring.",
  },
  rss: {
    en: "dbfb8c10",
    sv: "Läs ett RSS 2.0- eller Atom-flöde och skicka ut dess poster som rader. Kombinera det med en Intervall-trigger för att polla enligt schema: med dubblettrensning på (standard) minns det vilka poster som redan skickats ut och utlöser bara för NYA — så att en blogg, ett släppflöde, en podd eller en nyhetskälla driver flödet en gång per post. Den första pollningen sätter en utgångspunkt och skickar ut ingenting (den börjar bevaka från nu, inte hela historiken). Båda dialekterna normaliseras till samma kolumner: id, title, link, published (RFC3339 när flödets datum går att tolka), author, summary, content. Slå av dubblettrensningen för att bara tolka det aktuella flödet till rader vid varje körning.",
  },
  run_on_runner: {
    en: "6a10c74c",
    sv: "Kör ett skript på en av organisationens egna maskiner — en server, en laptop, vad som helst som kör Dazyflows körnodsagent. Använd det när arbetet behöver ett bibliotek, ett verktyg eller ett nät som de inbyggda stegen inte når. Välj en maskin i listan, eller en etikett som flera maskiner delar så att vilken ledig maskin som helst kan ta jobbet. Välj vad som startar skriptet — maskinens eget skal, sh, bash, Python, PowerShell eller Node — och skriv skriptet i rutan; eller koppla in det på ingången 'script' för att bygga det i ett tidigare steg. Värdet som kopplas in i 'in' kommer in på skriptets standard input; det skriptet skriver ut kommer tillbaka på 'out'. En avslutningskod som inte är noll gör att steget misslyckas, med skriptets felutskrift bifogad.",
  },
  secret_set: {
    en: "0b7ba489",
    sv: "Spara ett värde i din organisations krypterade hemlighetslager under det angivna namnet. Kombinera det med mallsubstitution (${secret.namn}) för att läsa tillbaka värdet i senare flödeskörningar — det klassiska användningsfallet är att lagra en markör för pollande flöden som behöver minnas 'vad var det sista jag bearbetade' över omstarter.",
  },
  sheets_append_row: {
    en: "3769c2f6",
    sv: "Lägg till rader i ett Google-kalkylblad. Koppla in en radlista i ingången 'rows'; kolumnerna tas från ingången 'headers' eller härleds ur radernas nycklar. Varje objekt blir en rad. Sätt en 'mappning' för att välja vilket inkommande fält som fyller vilken kolumn i bladet (t.ex. frågerubrikerna i ett Google-formulärsvar → dina kolumner) — båda sidorna väljs i listor (fälten i posten från tidigare steg och bladets egna kolumner), och mappningens kolumner definierar sedan raden, i ordning.",
  },
  sheets_export_pdf: {
    en: "4fb84ca6",
    sv: "Gör ett Google-kalkylblad till en PDF-fil. Koppla utgången PDF till ett steg som tar filer — t.ex. Bilagor i Gmail för att mejla den. Filen ligger i körningens tillfälliga utrymme (avancerat: ändra filnamnet med 'path').",
  },
  sheets_read_range: {
    en: "77fb3445",
    sv: "Läs ett område i ett Google-kalkylblad. Första raden blir kolumnrubriker (om inte headers=false), och varje följande rad blir ett objekt med rubrikerna som nycklar. Klistra in bladets URL eller dess ID.",
  },
  sheets_update_cells: {
    en: "87a5df6a",
    sv: "Ändra celler i rader som redan finns i bladet, i stället för att lägga till nya. Slå på 'Ta med radnummer' i steget Läs område, behåll raderna du agerat på och skicka hit dem tillsammans med de kolumner du vill ändra — varje rad skrivs tillbaka till den rad den kom från. Så här markerar ett flöde arbete som utfört (Status = Fakturerad, Påmind = idag) så att nästa körning hoppar över det. Kolumner som inte listas lämnas orörda, och en kolumn som bladet inte har ännu läggs till sist.",
  },
  site_check: {
    en: "a58e1975",
    sv: "Bevaka en sajt och få veta det bara när något faktiskt ändras. Kombinera med en Intervall-trigger: Gick ner utlöses vid den kontroll där sajten slutar svara ordentligt, Kom tillbaka när den svarar igen, och ingenting utlöses medan läget är oförändrat — så en sajt som varit nere i en timme larmar inte tolv gånger. En sajt som redan är nere vid allra första kontrollen utlöser dock, för det är en nyhet. Du kan också kräva att en viss fras finns på sidan, vilket fångar servern som svarar 200 med en felsida.",
  },
  slack_list_channels: {
    en: "5b42dfee",
    sv: "Hämta listan över kanaler som din Slack-bot ser, som en rad per kanal. Koppla utgången Kanaler till ett För varje för att göra något per kanal — till exempel skicka samma meddelande till alla rum boten är med i.",
  },
  slack_on_mention: {
    en: "b108818c",
    sv: "Startar det här flödet varje gång någon @-nämner din bot i Slack. Meddelandet, vem som skickade det och var det skickades finns som utgångar att koppla till nästa steg — t.ex. svara, logga förfrågan i ett kalkylblad eller vidarebefordra den med e-post. Sätt 'Bara i kanal' för att reagera i ett enda rum.",
  },
  slack_send_message: {
    en: "3f8e5bf5",
    sv: "Skicka ett meddelande till en Slack-kanal. Kanal och Meddelande kan skrivas på steget eller kopplas in från ett annat steg (en inkopplad ingång vinner över det skrivna värdet) — praktiskt för att skicka en rad från ett kalkylblad, en mejlsammanfattning eller vilken text som helst från ett tidigare steg rakt in i Slack.",
  },
  smhi_current: {
    en: "af8e7204",
    sv: "Slå upp vädret just nu för en punkt i Norden (och området omkring) med SMHI:s kostnadsfria Open Data-prognos — inget konto och ingen API-nyckel. Ge den en koordinat — skriv Latitud och Longitud, eller koppla in ett \"lat,lon\"-värde från ett Plats-/geokodningssteg — och du får tillbaka en Sammanfattning på en rad plus Temperaturen och ett Väderläge-ord (Clear, Rain, Snow …) som du kan förgrena på, allt i metriska enheter.",
  },
  smhi_forecast: {
    en: "951a12d2",
    sv: "Se upp till 10 dagar framåt för en punkt i Norden (och området omkring) med SMHI:s kostnadsfria Open Data-prognos — inget konto och ingen API-nyckel. Ge den en koordinat — skriv Latitud och Longitud, eller koppla in ett \"lat,lon\"-värde från ett Plats-/geokodningssteg — och välj antal dagar. Du får en läsbar Sammanfattning dag för dag plus arrayen Per dygn som JSON (min-/maxtemperatur och väderläge per dag), i metriska enheter.",
  },
  sort_rows: {
    en: "9aecc532",
    sv: "Sortera rader på en eller flera kolumner. Parametern 'by' är en kommaseparerad lista med kolumnnamn i prioritetsordning — tidigare namn väger tyngst, senare avgör lika fall. Sätt '-' före ett namn för fallande ordning: \"revenue,-created_at\" är intäkt i stigande ordning, därefter nyast först. (En äldre array med namn eller {column,desc:true}-objekt tas fortfarande emot för gamla flöden.)",
  },
  split_rows: {
    en: "8d782402",
    sv: "Dela en radström i två med ett CEL-villkor. Rader där filtret blir sant går ut på 'matched'; resten går ut på 'unmatched'. Samma formel som filtret i steget Lägg till en beräknad kolumn — `row.active && row.score >= 50` och liknande. Använd det när du annars skulle behöva Välj och byt namn på kolumner två gånger (en gång med filtret, en gång med dess negation): det här steget går igenom indatat en gång och ger dig båda halvorna utan extra kostnad.",
  },
  sqlite_insert_rows: {
    en: "3ac6f43f",
    sv: "Spara rader i en databasfil som ligger i din arbetsyta — ingen server, ingen anslutningssträng och ingen uppsättning behövs (det här är den enkla databasen; ta Postgres/MySQL bara om du redan har en). Tabellen skapas som standard automatiskt utifrån radernas form; slå av create_table om du redan satt upp ett schema du inte vill skriva över.",
  },
  sqlite_query: {
    en: "4c38d4e9",
    sv: "Kör en SELECT mot en SQLite-fil i din arbetsyta och få tillbaka rader. Använd ?-platshållare i SQL:en och skicka värdena genom params-arrayen, så att data från användare escapas säkert.",
  },
  sqlite_upsert_rows: {
    en: "1e26b791",
    sv: "Infoga eller uppdatera (upsert) rader i en SQLite-tabell i din arbetsyta. Ange konfliktkolumnerna — SQLite matchar befintliga rader på dem och uppdaterar dem på plats, medan nya rader infogas. Välj vilka kolumner som uppdateras vid en träff om du vill bevara några befintliga värden.",
  },
  stripe_cancel_subscription: {
    en: "84aff8f7",
    sv: "Avsluta en prenumeration med dess sub_…-id. Som standard fortsätter den gälla till slutet av den innevarande betalperioden (kunden får det den betalat för); sätt 'När den ska avslutas' till Omedelbart för att avsluta direkt. Koppla in id:t från Lista prenumerationer ('first_id') eller ett supportformulär, och lägg ett godkännandesteg före det här för det klassiska flödet 'godkänn i Slack → avsluta'. 'Slutar' kommer ut som ett datum till bekräftelsemeddelandet.",
  },
  stripe_create_customer: {
    en: "f0b1e387",
    sv: "Skapa en kund i ditt Stripe-konto. E-post, Namn och Beskrivning kan skrivas på steget eller kopplas in från ett tidigare steg (motsvarande ingång vinner över parametern). Den nya kundens id kommer ut på utgången Kund-ID för senare Stripe-steg; omförsök återanvänder samma Idempotency-Key, så en skakig körning kan inte skapa dubbletter.",
  },
  stripe_create_payment_link: {
    en: "4350cdc4",
    sv: "Skapa en betallänk för ett av dina Stripe-priser. Välj priset på steget (hämtat från ditt konto) eller koppla in ett price_…-id från ett tidigare steg — ingången vinner över parametern, t.ex. ett pris per rad från ett kalkylblad. URL:en till Stripes kassasida kommer ut på utgången URL — det klassiska flödet är ny orderrad → betallänk → mejla/slacka den. Antal kan också kopplas in; omförsök återanvänder samma Idempotency-Key, så en skakig körning kan inte skapa dubbletter av länken.",
  },
  stripe_create_refund: {
    en: "3a7b0e35",
    sv: "Återbetala en betalning med dess payment_intent-id (pi_…). Lämna Belopp tomt för full återbetalning, eller ange det i valutans minsta enhet (cent/öre) för en delvis — både id och belopp kan kopplas in från ett tidigare steg, t.ex. fälten i ett supportformulär. Lägg ett godkännandesteg före det här för det klassiska flödet 'godkänn i Slack → återbetala'. Omförsök återanvänder samma Idempotency-Key, så en skakig körning inte kan återbetala två gånger.",
  },
  stripe_get_customer: {
    en: "172d65c9",
    sv: "Hämta en enskild kund med dess Stripe-id. Varje prenumerations- och betalningshändelse bär ett cus_…-id i stället för en e-postadress, så det här är steget som ger dig någon att skriva till: koppla triggerns Kund rakt in i Kund här, och dess E-post in i ett Skicka e-post-steg. (Vill du söka på e-post i stället? Använd Sök kunder — Stripes sökning kan inte slå upp på id.)",
  },
  stripe_list_events: {
    en: "32ddf3ef",
    sv: "Lista de senaste händelserna på ditt konto (nyast först), med möjlighet att filtrera på särskilda typer som payment_intent.succeeded eller invoice.payment_failed. För en trigger bygger du: Schema/Intervall → det här steget (koppla in den sparade markören i 'Efter ID') → För varje händelse → … → Ange hemlighet med 'Sista ID'. Bara händelser nyare än markören returneras, så varje pollning ser varje händelse en gång. Utan markör returneras de allra senaste händelserna.",
  },
  stripe_list_subscriptions: {
    en: "20256380",
    sv: "Lista prenumerationer, antingen för en enskild kund (koppla in ett cus_…-id från Sök kunder) eller över hela kontot när Kund är tomt — t.ex. en daglig genomgång av past_due. Träffarna kommer ut som en JSON-lista på 'subscriptions'; 'first_id' bär den första träffens sub_…-id, så det vanliga fallet med en enda prenumeration kan kopplas direkt till Avsluta prenumeration utan ett För varje.",
  },
  stripe_on_payment: {
    en: "c19d1a06",
    sv: "Startar flödet när en betalning lyckas i ditt Stripe-konto (webhook-händelsen payment_intent.succeeded). Uppsättning: lägg i Stripes kontrollpanel till en webhook-ändpunkt som pekar på https://<din-dazyflow-värd>/api/v1/events/stripe/<tenant>, prenumerera på payment_intent.succeeded och spara sedan ändpunktens signeringshemlighet (whsec_…) som en hemlighet med namnet STRIPE_WEBHOOK_SECRET — varje leverans Stripe-Signature verifieras mot den. Skickar ut beloppet (i minsta enhet och i en visningsform som '49.99 USD'), valuta, betalarens e-post, beskrivning och råhändelsen. Vill du polla i stället för webhooks bygger du Schema → 'Lista händelser'.",
  },
  stripe_on_payment_failed: {
    en: "ce0e0582",
    sv: "Startar flödet när ett betalningsförsök misslyckas i ditt Stripe-konto (webhook-händelsen payment_intent.payment_failed). Uppsättningen använder samma ändpunkt som betalningstriggern: peka en Stripe-webhook mot https://<din-dazyflow-värd>/api/v1/events/stripe/<tenant>, prenumerera på payment_intent.payment_failed och spara ändpunktens signeringshemlighet (whsec_…) som en hemlighet med namnet STRIPE_WEBHOOK_SECRET. Utgången Orsak till fel bär Stripes nekandemeddelande ('Your card was declined.') — koppla den tillsammans med beloppet och betalarens e-post till ett aviseringssteg.",
  },
  stripe_on_subscription_canceled: {
    en: "e1c7381a",
    sv: "Startar flödet när en prenumeration avslutas i ditt Stripe-konto (webhook-händelsen customer.subscription.deleted — den utlöses när prenumerationen verkligen tar slut, så en avslutning som schemalagts till periodens slut utlöser då). Uppsättningen använder samma ändpunkt som betalningstriggern: peka en Stripe-webhook mot https://<din-dazyflow-värd>/api/v1/events/stripe/<tenant>, prenumerera på customer.subscription.deleted och spara ändpunktens signeringshemlighet (whsec_…) som en hemlighet med namnet STRIPE_WEBHOOK_SECRET. Skickar ut prenumerationens och kundens id, planens namn och när den slutade — koppla kundens id till Sök kunder för att få e-postadressen.",
  },
  stripe_search_customers: {
    en: "595798bf",
    sv: "Sök bland dina kunder med Stripes frågesyntax, t.ex. email:'a@b.com' eller metadata['crm_id']:'acct_42'. Koppla in ett värde i ingången Fråga för uppslag per körning (e-postfältet i ett supportformulär, en kolumn i ett kalkylblad). Alla träffar kommer ut som en JSON-lista på 'customers' (loopa den med För varje för att agera på var och en); 'first_id' bär den första träffens cus_…-id, så det vanliga uppslaget med en enda träff kan kopplas direkt till en Kund-ingång (Skicka faktura, Lista prenumerationer) utan ett För varje, och 'first_email' är praktiskt för ett aviseringssteg.",
  },
  stripe_send_invoice: {
    en: "ed224d26",
    sv: "Skapa en faktura med en rad för en kund och låt Stripe mejla den (en sida hos Stripe där kunden kan betala med kort eller överföring). Ett steg täcker hela API-sekvensen — utkast, fakturarad, slutför, skicka — och en omkörning spelar upp redan klara delsteg i stället för att fakturera dubbelt. Beloppet anges i valutans minsta enhet (12000 = 120,00); kund, belopp och beskrivning kan alla kopplas in från ett tidigare steg, t.ex. en ny rad i ett orderblad. Det klassiska flödet: ny orderrad → Sök kunder (eller Skapa kund) → Skicka faktura → avisera. Observera: Stripe levererar bara fakturamejl i skarpt läge; i testläge får du kolla kontrollpanelen.",
  },
  subgraph: {
    en: "889c1424",
    sv: "Kör ett annat flöde (med ID, i samma arbetsyta) som ett enda steg. Ingångarna på det här steget matas in på bestämda steg i barnflödet via input_map; bestämda utgångar i barnflödet blir det här stegets utgångar via output_map. Arbetaren startar barnflödet asynkront; föräldern parkeras tills barnet är klart.",
  },
  switch: {
    en: "02a7e5ff",
    sv: "Dirigerar nyttolasten på `in` till en av N fallportar genom att matcha en nyckel mot varje falls värde. Parametern `cases` är en ordnad lista med {slot, equals} — det FÖRSTA fallet vars värde matchar nyckeln vinner, och hela nyttolasten går ut på den utgången; en nyckel som inte matchar något fall går till `default`. Matcha hela indatat, eller ett fält i det med parametern `field`. Ett `equals` som är en lista matchar om nyckeln är lika med något element (som Jämförs one_of). Flervalssyskonet till Förgrening — ta det i stället för att kedja Förgreningar när du ska dela upp en nyttolast på status/enum/kategori.",
  },
  text: {
    en: "0f934db1",
    sv: "Skickar ut ett fast textvärde. Parametern 'text' kan vara flera rader; senare steg ser det som text/plain på porten 'out'.",
  },
  twilio_send_sms: {
    en: "7b1bb480",
    sv: "Skicka ett SMS via Twilio. Mottagaren ('Till') och meddelandet ('Innehåll') kan skrivas på steget eller kopplas in från ett tidigare steg (motsvarande ingång vinner över parametern). Skicka från ett av dina Twilio-nummer ('Från', i E.164 som +15551234567) eller ange ett Messaging Service SID i stället. Anslut ditt Twilio-konto en gång på Appar-sidan.",
  },
  unwrap_results: {
    en: "f09a5402",
    sv: "Platta ut ett För varje-stegs `results`-lista tillbaka till vanliga rader. För varje kör sitt loopinnehåll per post, så varje resultat omsluter innehållets utdata per steg ({status, nodes:{<id>:{output:{port:val}}}}); tabellstegen längre fram vill ha ett stegs faktiska utdata, inte omslaget. Samla resultat plockar ut en utgångsport från ett steg i loopinnehållet ur varje resultat och skickar ut dem som rader — så att en kedja som Sök mejl → För varje (Läs mejl) → Samla resultat från loopen → Lägg till en beräknad kolumn till sist låter beräkningen se `row.headers.From`. Med ett loopinnehåll på ett enda steg som bara har en utgång kan du lämna både `node` och `port` tomma. Misslyckade poster hoppas över som standard (För varje-stegets `errors`-utgång bär dem fortfarande).",
  },
  url: {
    en: "f7780718",
    sv: "Håll en webbadress — skriv den direkt eller koppla in en sträng i ingången 'url' — och skicka ut den på 'out' först efter en kontroll att det är en riktig http(s)-URL. En felaktig adress (utan protokoll, utan värd, eller med ett annat protokoll än http) får steget att misslyckas direkt i stället för att haverera i ett senare steg. Adressen avkodas också så att du kan agera på delarna utan strängfiffel: 'host' (example.com), 'path' (/blogg/inlagg) och 'query' som en tabell (?a=1&b=2 → {a:\"1\",b:\"2\"}, värdena URL-avkodade, första värdet vinner vid upprepad nyckel) — så att du kan förgrena på sökvägen, använda en parameter i en mall eller bygga om en URL direkt.",
  },
  weather_current: {
    en: "44ad3e66",
    sv: "Slå upp vädret just nu för en punkt på kartan. Ge den en koordinat — skriv Latitud och Longitud, eller koppla in ett \"lat,lon\"-värde från ett annat steg (en geokodning, ett formulärfält, en enhets GPS) — och du får tillbaka en Sammanfattning på en rad plus Temperaturen och ett Väderläge-ord (Clear, Rain, Snow …) som du kan förgrena på, samt hela observationen som JSON. Använder OpenWeathers kostnadsfria Current Weather API — vilken vanlig nyckel som helst fungerar, ingen betald prenumeration.",
  },
  weather_forecast: {
    en: "94af8f73",
    sv: "Se några dagar framåt för en punkt på kartan. Ge den en koordinat — skriv Latitud och Longitud, eller koppla in ett \"lat,lon\"-värde från ett annat steg — och välj antal dagar (1–5). Du får en läsbar Sammanfattning dag för dag plus arrayen Per dygn som JSON (min-/maxtemperatur, väderläge och regnrisk per dag), sammanställd från OpenWeathers kostnadsfria 5-dygnsprognos i 3-timmarssteg. Vilken vanlig nyckel som helst fungerar, ingen betald prenumeration.",
  },
  web_watch: {
    en: "59b51546",
    sv: "Håll ett öga på en webbsida och låt flödet köra bara när den faktiskt ändras — ett pris, en statussida, en upphandlingslista, en jobbannonssida. Kombinera med en Intervall-trigger. Första kontrollen registrerar tyst vad sidan säger idag; från och med då jämför varje kontroll. Steg som är kopplade till Vid ändring ligger vilande så länge inget ändras, så ett larm går bara ut när det finns något att säga. Som standard jämförs orden på sidan, inte HTML:en bakom dem, vilket hindrar osynliga ändringar i markup från att slå falskt alarm. Vill du bevaka ett enda tal i stället för hela sidan anger du ett mönster i 'Bevaka bara detta'.",
  },
  webhook_input: {
    en: "793f69eb",
    sv: "Startar flödet när något skickas till dess webbadress — ett inskickat svar från flödets publicerade formulär, eller en HTTP-förfrågan från ett annat system. Innehåll är det som skickades (formulärfält / JSON); Rubriker bär förfrågans metadata.",
  },
  webhook_send: {
    en: "fa973403",
    sv: "Skicka data till en webhook-URL — den utgående motsvarigheten till webhook-triggern. URL och Innehåll kan skrivas på steget eller kopplas in från ett tidigare steg (motsvarande ingång vinner över parametern); text skickas som den är, ett objekt eller en lista skickas som JSON. Adresser i privata nät är blockerade som standard.",
  },
};
