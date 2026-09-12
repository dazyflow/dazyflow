// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

import type { DescriptionMap } from "../../lib/dropText";

// Swedish prose for the Apps pages: each integration's friendly description and
// its collapsible technical notes.
//
// Keys are '<slug>.description' and '<slug>.technical_notes'. `en` is the
// descriptionFingerprint of the English in integrationMeta.ts each translation
// was made from — the same drift guard as the drop descriptions, and it matters
// more here, because this English lives in the frontend and changes without a
// catalog rebuild.
//
// Product names, env vars, scopes, endpoints and header names stay English:
// they are what the reader will type into a dashboard or grep for in a log.
export const SV_INTEGRATION_PROSE: DescriptionMap = {
  "46elks.description": {
    en: "9bc17c9e",
    sv: 'Skicka SMS direkt från ett flöde via 46elks, en svensk meddelandeleverantör som är populär i hela Norden. Skicka från ett alfanumeriskt avsändarnamn (som "Acme") för envägsaviseringar — orderuppdateringar, påminnelser, verifieringskoder — eller från ett av dina 46elks-nummer när du vill att mottagaren ska kunna svara. Med en testkörningsknapp kan du validera ett meddelande utan att skicka det eller bli fakturerad.',
  },
  "46elks.tagline": {
    en: "59f0b527",
    sv: "Skicka svenska sms, och ring samtal.",
  },
  "46elks.technical_notes": {
    en: "9005d254",
    sv: "Autentiseras med ditt 46elks-API-användarnamn och lösenord (HTTP Basic), som anges en gång som 46elks-anslutningen på den här sidan (lagras krypterat som conn.46elks.*) och matas in vid körning — inga uppgifter på steget eller i flödet. Skickar en formulärkodad POST till 46elks ändpunkt /a1/sms. 'Från' är antingen E.164 (går att svara på) eller ett alfanumeriskt avsändar-ID (max 11 tecken, måste innehålla en bokstav, går inte att svara på). 46elks har ingen idempotensnyckel, så steget gör aldrig automatiska omförsök och motorn rensar dubbletter i återupptagna körningar — ett nytt utskick skulle faktureras dubbelt.",
  },
  "calendar.description": {
    en: "8b649743",
    sv: "Läs och skriv i en kalender som inte är Googles — Fastmail, iCloud, Nextcloud, eller en du kör själv. Lista morgondagens bokningar och meddela alla, eller lägg in ett samtal i kalendern direkt från ett formulär.",
  },
  "calendar.tagline": {
    en: "5f5be9d4",
    sv: "Skapa och läs händelser i vilken kalender du än använder.",
  },
  "calendar.technical_notes": {
    en: "f05734c4",
    sv: 'Kontot — serveradress, användarnamn och lösenord (eller ett app-lösenord hos en leverantör med tvåfaktorsinloggning) — ställs in en gång här och matas in i varje Calendar-steg vid körning; lösenordet ligger i den krypterade hemlighetslagringen. Adressen kan vara en upptäcktsrot, ett principal, eller en enskild kalenders egen sökväg: klienten går från det din leverantör publicerade ner till kalendersamlingarna under, eftersom ingen användare kan förväntas veta vilket av dem de fått. Om kontot har flera kalendrar, ange den du vill använda — Testa anslutning listar dem om du inte gör det. Händelser kommer ut i samma form som Google Calendar-steget ger, så ett flöde flyttas mellan de två genom att byta steg. Tidsfönster tar samma relativa former ("tomorrow", "+7d", "tomorrow+9h"), tolkade i den tidszon du anger.',
  },
  "chatgpt.description": {
    en: "c65b2822",
    sv: "Kör prompter genom ChatGPT, OpenAI:s AI-assistent. Använd den på samma sätt som Claude — sammanfatta text, klassificera indata, extrahera fält eller skriv utkast till svar — där du hellre använder en OpenAI-modell.",
  },
  "chatgpt.tagline": {
    en: "7f10b835",
    sv: "Skriv utkast, klassificera och svara på frågor med ChatGPT.",
  },
  "chatgpt.technical_notes": {
    en: "455a4aed",
    sv: "OpenAI:s Chat Completions API, autentiserat med API-nyckeln som är satt på den här anslutningen — flödena hämtar den automatiskt, ingen nyckel på steget. De strukturerade stegen (Extrahera fält, Klassificera) använder OpenAI:s function tool-calls.",
  },
  "claude.description": {
    en: "2a278926",
    sv: "Kör prompter genom Claude, Anthropics AI-assistent. Användbart för att sammanfatta text från tidigare steg, klassificera indata, generera svar, eller var som helst i flödet där du vill ha en språkmodell med i loopen.",
  },
  "claude.tagline": {
    en: "edd26af1",
    sv: "Skriv, sammanfatta och tolka text med Claude.",
  },
  "claude.technical_notes": {
    en: "2ac221d3",
    sv: "Autentiseras med API-nyckeln som är satt på den här anslutningen — flödena hämtar den automatiskt, ingen nyckel på steget. För lokal utveckling utan nyckel dirigerar flaggan dzd --claude-cli anropen genom en lokal `claude -p`-CLI plus en MCP-server, så att flöden kan köra chattvägen mot din redan inloggade CLI.",
  },
  "collections.description": {
    en: "17018173",
    sv: "Spara rader i en inbyggd samling utan någon uppsättning, och läs dem tillbaka — det är lagringen bakom Samlingar-sidan i appen. Ta det för att samla ett flödes utdata för granskning, bygga en enkel instrumentpanel, eller hålla löpande summor utan att sätta upp en riktig databas.",
  },
  "collections.tagline": {
    en: "24edbe95",
    sv: "Spara rader nu, och hämta upp dem sen.",
  },
  "discord.description": {
    en: "acd35112",
    sv: "Lägg upp meddelanden i en Discord-kanal från ett flöde — en ping när driftsättningen är klar, en avisering när bygget går sönder, en daglig sammanfattning, eller ett tips till teamet i samma stund något händer. Sätt avsändarnamn och avatar per meddelande om du vill.",
  },
  "discord.tagline": {
    en: "7f234ea7",
    sv: "Skriv i en server, och reagera på vad folk skriver.",
  },
  "discord.technical_notes": {
    en: "f4c9e119",
    sv: "Lägger upp via en webhook-URL till en Discord-kanal, som anges en gång som Discord-anslutningen på den här sidan (lagras krypterad som conn.discord.webhook_url) — ingen bot och ingen OAuth-app behövs. Skapa den under Serverinställningar → Integrationer → Webhooks. Valfria överskrivningar av användarnamn och avatar per meddelande.",
  },
  "email.description": {
    en: "9add698b",
    sv: "Skicka mejl från ditt eget mejlkonto: en daglig sammanfattning, en avisering, eller ett svar som ett tidigare steg skrivit. Bifoga filer som flödet skapat på vägen, och skicka ett per mottagare från en lista.",
  },
  "email.tagline": {
    en: "9ef089c9",
    sv: "Skicka ett mejl till vem som helst, med bilagor om du behöver.",
  },
  "email.technical_notes": {
    en: "3492a61e",
    sv: "E-postservern — värd, port, säkerhet (STARTTLS på 587 / implicit TLS på 465 / ingen), användarnamn, lösenord och Från-adress — konfigureras en gång här och matas in i varje E-post-steg vid körning; lösenordet ligger i det krypterade hemlighetslagret. Använd 'Testa anslutningen' för att bekräfta server och inloggning innan du sparar.",
  },
  "excel.description": {
    en: "b17f00b1",
    sv: "Läs in .xlsx-arbetsböcker som rader, och skriv rader tillbaka som en ny arbetsbok. Användbart när någon lägger en fil i arbetsytan och du vill städa den, koppla den mot en referenstabell eller läsa in den i en riktig databas.",
  },
  "excel.tagline": {
    en: "387fad29",
    sv: "Läs en arbetsbok, och skriv nya rader tillbaka in i den.",
  },
  "excel.technical_notes": {
    en: "7a2f56f6",
    sv: "Bygger på biblioteket excelize. Kontraktet med rader + rubriker matchar Sheets och databasdropparna, så en Excel-fil kan matas direkt in i en Postgres-upsert med ett map_rows emellan.",
  },
  "fortnox.description": {
    en: "703b2c13",
    sv: "Hantera kunder och fakturor i Fortnox, Sveriges ledande bokföringsplattform för småföretag. Skapa en kund från en anmälan, ställ ut en faktura till den, och välj vem som ska faktureras i en sökbar lista över dina befintliga kunder. Polla fakturor på status för att bygga ett flöde som reagerar på nyligen betalda fakturor — ett tackmejl, ett leveranssteg — eller påminner om förfallna.",
  },
  "fortnox.tagline": {
    en: "83e92ea7",
    sv: "Sköt fakturor och kunder i bokföringen, helt automatiskt.",
  },
  "fortnox.technical_notes": {
    en: "0f749f7f",
    sv: 'Fortnox OAuth 2.0 (authorize hos apps.fortnox.se/oauth-v1) med scope per resurs — customer, invoice och companyinformation täcker de steg som finns. Token-ändpunkten använder client_secret_basic (uppgifterna i en HTTP Basic-rubrik), och refresh-tokens roterar vid varje förnyelse; daemonen sparar den roterade token och förnyar vid utgång, så långlivade flöden fortsätter fungera — men ett konto som stått stilla längre än Fortnox fönster för refresh-tokens (~31 dagar) måste anslutas om. Anrop och svar använder Fortnox singulara PascalCase-hölje ({"Customer":…}, {"Invoice":…}). Fortnox har ingen idempotensnyckel, så skapa-stegen gör inga automatiska omförsök (ett omförsök skulle ge dubbletter); och inga webhooks, så \'utlös vid betald faktura\' byggs som Schema → Lista fakturor (filter=fullypaid) → För varje → dubblettrensning på DocumentNumber.',
  },
  "spotify.description": {
    en: "d0b8cc59",
    sv: "Läs vad ett anslutet Spotify-konto följer. Artisterna någon följer kommer ut som en vanlig lista med poster — namn, genrer, en länk att öppna var och en — så att ett flöde kan utgå från musiken de faktiskt lyssnar på i stället för en bevakningslista som förts för hand: stäm av den mot en konsertsökning, logga den till ett kalkylblad, eller mejla ett veckobrev om vad som är nytt.",
  },
  "spotify.tagline": {
    en: "d238b002",
    sv: "Slå upp låtar och album, och se vad som spelas.",
  },
  "spotify.technical_notes": {
    en: "ea71769a",
    sv: "Spotify OAuth 2.0 (authorize hos accounts.spotify.com), token-ändpunkten använder client_secret_basic, och scopet user-follow-read täcker det steg som finns. Access-tokens lever en timme och förnyas automatiskt, så inget steg erbjuder en ruta att klistra in en token i.\n\nTvå gränser avgör vad som är värt att bygga. En Spotify-app stannar i **development mode** om inte ägaren kvalificerar sig för extended quota — ett registrerat företag med en lanserad tjänst och 250 000 månatliga användare — och development mode tillåter som mest **5 lyssnare**, var och en manuellt tillagd i Spotifys dashboard, med Premium som krav för appens ägare. Och Spotify har inga webhooks, så allt som är händelseformat måste pollas: Schema → Följda artister → För varje → dubblettrensning. Kvoten räknas per utvecklarkonto (5 000 anrop/dygn, 5/s), så en daglig takt är bättre än en timvis.\n\nSpotify drog också in en stor del av API:et i november 2024 och februari 2026 — audio features och analysis, rekommendationer, relaterade artister, nya släpp, alla batch-hämtningar, och fälten popularity och followers — så flöden som analyserar lyssnande går inte att bygga på dagens API, oavsett kvot.",
  },
  "gemini.description": {
    en: "6b44b880",
    sv: "Kör prompter genom Gemini, Googles AI-modell. Använd den på samma sätt som Claude eller ChatGPT — sammanfatta text, klassificera indata, extrahera fält, skriv utkast till svar — när du hellre vill använda en Google-modell, eller redan har en nyckel från Google AI Studio.",
  },
  "gemini.tagline": {
    en: "9be979b3",
    sv: "Arbeta med text och bilder med Googles Gemini.",
  },
  "gemini.technical_notes": {
    en: "6f0f3bbd",
    sv: "Googles Gemini-API (generateContent), autentiserat med API-nyckeln som är satt på den här anslutningen — flöden hämtar den automatiskt, ingen nyckel på steget. Nyckeln skickas som en x-goog-api-key-header i stället för som frågeparameter, så den hamnar inte i proxyloggar. Strukturerade steg (Extrahera fält, Klassificera) använder Geminis function calling i läget ANY, vilket tvingar fram anropet. Hämta en nyckel i Google AI Studio; den kostnadsfria nivån är hastighetsbegränsad snarare än otillgänglig, så ett flöde med mycket trafik kan behöva ett projekt med fakturering.",
  },
  "git.description": {
    en: "c69a927c",
    sv: "Klona repon och checka ut grenar inne i din arbetsyta. Ta det när ett flöde behöver granska källkod, hämta mallar från ett känt repo, eller lägga upp filer innan ett annat steg arbetar på dem.",
  },
  "git.tagline": {
    en: "95f1ec42",
    sv: "Läs ett repo, och committa tillbaka till det från ett flöde.",
  },
  "git.technical_notes": {
    en: "2959c2a2",
    sv: "Alla operationer hålls inne i arbetsytans sandlåda via normalisering av sökvägar — kloner skriver in i sandlådans rot, aldrig ovanför. Skrivskyddat i dag; skrivoperationer mot fjärrepon stöds inte.",
  },
  "github.description": {
    en: "3c48c703",
    sv: "Skapa ärenden, kommentera befintliga och starta flöden vid push eller ny PR. Vanliga mönster: dirigera en inkommande avisering till ett spårat ärende, lägg upp en driftsättningsavisering när commits landar på main, starta ett triage-flöde när någon öppnar en PR.",
  },
  "github.tagline": {
    en: "d1ae8e1d",
    sv: "Skapa ärenden, kommentera, och reagera på det som landar i ditt repo.",
  },
  "github.technical_notes": {
    en: "8e75840a",
    sv: "Personliga åtkomsttokens eller OAuth-användartokens via Authorization: Bearer. Webhook-triggrarna använder GitHubs HMAC-schema X-Hub-Signature-256 — peka repots webhook-URL på /api/v1/events/github/<tenant>, med webhookens Secret lika med DAZYFLOW_GITHUB_WEBHOOK_SECRET. API-versionen är låst till 2022-11-28.",
  },
  "gmail.description": {
    en: "3a789bcd",
    sv: "Skicka mejl, sök i din inkorg och läs hela meddelandetexter. Det klassiska användningsfallet: reagera på inkommande mejl när de kommer — kombinera sök-steget med en pollningstrigger, så minns flödet vilka meddelanden det redan bearbetat och gör inte om samma arbete.",
  },
  "gmail.tagline": {
    en: "55341025",
    sv: "Skicka mejl, och starta ett flöde i samma stund ett kommer in.",
  },
  "gmail.technical_notes": {
    en: "2fb6701f",
    sv: "Gmail API + Google OAuth. access_type=offline + prompt=consent följer med vid authorize så att refresh_token finns kvar mellan körningar. Markörens dubblettrensning lagras i det krypterade hemlighetslagret via secret_set + ${secret.…}-substitution och överlever omstarter av daemonen.",
  },
  "google-calendar.description": {
    en: "fc0aa518",
    sv: "Skapa kalenderhändelser och lista vad som är på gång. Lägg in ett möte i en kalender när ett flöde utlöses, gör en inkommande bokning till en händelse, eller hämta dagens schema till en morgonsammanfattning.",
  },
  "google-calendar.tagline": {
    en: "3f0abe75",
    sv: "Lägg in händelser i kalendern, och se vad som är på gång.",
  },
  "google-calendar.technical_notes": {
    en: "85ee9aa6",
    sv: "Delar OAuth-klienten 'google' med Gmail, Sheets och Forms — ett medgivande täcker alla, och att ansluta Calendar lägger bara till kalender-scopen. Tider anges som RFC3339 för händelser med klockslag eller som enkla datum för heldagshändelser; återkommande händelser expanderas till enskilda tillfällen i starttidsordning.",
  },
  "google-drive.description": {
    en: "1d269698",
    sv: "Lista, ladda ner och ladda upp filer i Google Drive. Hämta en fil för att mejla den som bilaga, arkivera ett inkommande dokument, plocka ut ett Doc eller Sheet som PDF, eller lägg tillbaka genererade filer i en mapp för ditt team.",
  },
  "google-drive.tagline": {
    en: "44430810",
    sv: "Hämta, spara och dela filer utan att öppna en mapp.",
  },
  "google-drive.technical_notes": {
    en: "c14de76f",
    sv: "Delar OAuth-klienten 'google' med Gmail, Sheets och Forms — ett medgivande täcker alla, och att ansluta Drive lägger bara till drive-scopen. Google-dokument (Docs/Sheets/Slides) har inga råa byte, så nedladdningsdroppen exporterar dem till ett konkret format (PDF som standard). Nedladdningar hamnar i körningens tillfälliga utrymme.",
  },
  "google-forms.description": {
    en: "171d447f",
    sv: "Starta ett flöde när ett Google-formulär får nya svar, där varje svar har frågans rubrik som nyckel — koppla det direkt till ett tillägg i Sheets för att logga inskickade svar, eller till vilket steg som helst som tar poster.",
  },
  "google-forms.tagline": {
    en: "32cd25d3",
    sv: "Gör varje formulärsvar till något som händer automatiskt.",
  },
  "google-forms.technical_notes": {
    en: "7a4d6aef",
    sv: "Delar OAuth-klienten 'google' med Gmail och Sheets; inkrementell auktorisering innebär att en anslutning av Forms bara begär forms.*-scopen (svar + formulärets innehåll, skrivskyddat), utan att Gmail/Sheets behöver godkännas igen. Triggern pollar forms.responses.list mot en markör per flöde i det krypterade hemlighetslagret.",
  },
  "google-sheets.description": {
    en: "80bb345b",
    sv: "Läs rader från ett kalkylblad och lägg till rader i det. Använd det för att hålla ett blad i synk med en databas, logga inkommande händelser så att icke-tekniska kollegor kan titta på dem, eller hämta in en referenstabell till andra flöden.",
  },
  "google-sheets.tagline": {
    en: "0e8f2970",
    sv: "Läs och lägg till rader, så håller sig kalkylarket uppdaterat själv.",
  },
  "google-sheets.technical_notes": {
    en: "4ed1377c",
    sv: "Delar OAuth-klienten 'google' med Gmail — ett medgivande täcker båda. Formen med rader + rubriker är utbytbar med Excel- och databasdropparna, så ett blad kan matas direkt in i en Postgres-upsert utan mellanliggande omvandlingar.",
  },
  "home-assistant.description": {
    en: "347c7486",
    sv: "Styr ditt smarta hem och reagera på vad det gör. Tänd lampor, lås en dörr, ställ termostaten eller kör en scen — och starta ett flöde automatiskt i samma stund en enhets status ändras, som att en dörr öppnas eller en sensor löser ut.",
  },
  "home-assistant.tagline": {
    en: "602e6c4e",
    sv: "Styr ditt hem, och låt hemmet starta dina flöden.",
  },
  "home-assistant.technical_notes": {
    en: "22c3a543",
    sv: "Pratar med din Home Assistant-instans över dess REST-API, med instansens URL och en långlivad åtkomsttoken (skapa en under Profil → Long-Lived Access Tokens) som konfigureras en gång här. En LAN-adress (homeassistant.local, 192.168.x.x) kräver att daemonens utgående trafik till privata nät är påslagen (DAZYFLOW_ALLOW_PRIVATE_EGRESS).",
  },
  "http.description": {
    en: "ee5c02b3",
    sv: "Nå en tjänst på webben som ännu inte har en egen app här. Ange en adress, och det som kommer tillbaka blir ett steg som resten av flödet kan bygga vidare på.",
  },
  "http.tagline": {
    en: "33e6403d",
    sv: "Anropa vilken tjänst som helst på webben, även en utan färdig app.",
  },
  "http.technical_notes": {
    en: "65bd043a",
    sv: "SSRF-skyddet blockerar loopback, RFC1918 och link-local (inklusive AWS instansmetadata på 169.254.169.254). Konfigurerbar gräns för svarsstorlek, filter på statuskod och tidsgräns för förfrågan. JSON-/text-MIME-detektering på svaret.",
  },
  "klarna.description": {
    en: "025ffa4d",
    sv: 'Hantera dina Klarna-ordrar direkt från ett flöde. Klarna är den nordiska "köp nu, betala senare"-kassan, och det här är baksidan av den: slå upp en order, ta betalningen när varorna skickas (helt eller delvis), och återbetala en retur. Kombinera återbetalningen med ett godkännandesteg för det klassiska flödet "nicka i Slack, återbetala sedan", eller kontrollera en orders status innan du agerar på den.',
  },
  "klarna.tagline": {
    en: "9fe66b55",
    sv: "Håll koll på ordrar och betalningar från din kassa.",
  },
  "klarna.technical_notes": {
    en: "d1248b5e",
    sv: "Autentiseras med ditt Klarna-API-användarnamn och lösenord (HTTP Basic), som anges en gång som Klarna-anslutningen på den här sidan (lagras krypterat som conn.klarna.*) och matas in vid körning — inga uppgifter på steget eller i flödet. Välj dataregion och miljö som en del av anslutningen (EU / Nordamerika / Oceanien × produktion eller playground), vilket väljer API-värden — den utgår från EU:s playground så att en halvkonfigurerad anslutning inte kan flytta riktiga pengar. Bygger på Klarnas Order Management API (v1): debitering och återbetalning POSTar JSON till /ordermanagement/v1/orders/{id}/captures|refunds och läser det nya id:t ur rubriken Capture-ID / Refund-ID (eller Location). Lämna beloppet tomt för att agera på hela det utestående beloppet (en GET fyller i det); belopp anges i valutans minsta enhet (öre/cent). Klarna har ingen pålitlig idempotensnyckel här, så debitering och återbetalning görs aldrig om automatiskt och motorn rensar dubbletter i återupptagna körningar — en upprepning skulle debitera eller återbetala dubbelt. Inga webhooks, så 'utlös vid ny order' byggs som Schema → Hämta order → förgrena på status.",
  },
  "mailbox.description": {
    en: "8d8816fb",
    sv: "Bevaka en brevlåda och agera på det som kommer in. Sök i en mapp efter de mejl du bryr dig om — från en viss avsändare, bara olästa, de senaste dagarna — och lämna varje meddelande vidare till resten av flödet. Fungerar med Fastmail, iCloud, en egen server, och Gmail med ett app-lösenord.",
  },
  "mailbox.tagline": {
    en: "139538c9",
    sv: "Bevaka en inkorg, och gör något med det som kommer in.",
  },
  "mailbox.technical_notes": {
    en: "5b2e1008",
    sv: 'Mejlkontot — server, port, säkerhet (implicit TLS på 993 / STARTTLS på 143 / ingen), användarnamn, lösenord och standardmapp — ställs in en gång här och matas in i varje Mailbox-steg vid körning; lösenordet ligger i den krypterade hemlighetslagringen. Medvetet skilt från Email-anslutningen (SMTP), eftersom de två använder olika servrar. Läsningar använder EXAMINE och BODY.PEEK, så en sökning markerar aldrig mejl som lästa. "Bara nya sedan förra körningen" följer mappens egna UIDVALIDITY och UID i stället för en tidsstämpel, så en publicerad pollning agerar på varje mejl exakt en gång och återhämtar sig rent om mappen någon gång skapas om. Använd "Testa anslutning" för att bekräfta server, inloggning och mappnamn innan du sparar. Observera att Microsoft 365 har stängt av lösenordsinloggning för IMAP: det kräver OAuth, vilket den här anslutningen ännu inte gör.',
  },
  "mqtt.description": {
    en: "525c6395",
    sv: "Publicera meddelanden till en MQTT-mäklare — den lätta ryggraden i de flesta hemautomations- och IoT-uppsättningar. Tänd en smart lampa, skicka ett kommando till en enhet, eller sänd ut en statusuppdatering som allt som prenumererar på ämnet plockar upp.",
  },
  "mqtt.tagline": {
    en: "ecd0ee39",
    sv: "Prata med sensorer och enheter i realtid.",
  },
  "mqtt.technical_notes": {
    en: "862d4b33",
    sv: "Mäklarens ändpunkt (tcp:// eller ssl://; bara värd:port blir tcp://…:1883) och valfritt användarnamn/lösenord anges en gång som MQTT-anslutningen på den här sidan (lagras krypterat som conn.mqtt.*) och matas in vid körning. Stöder QoS-nivåer och retain-flaggan. Mäklare i privata nät är blockerade om inte operatören tillåter utgående trafik dit (DAZYFLOW_ALLOW_PRIVATE_EGRESS).",
  },
  "mysql.description": {
    en: "bd3fe5c0",
    sv: "Infoga, uppdatera och läsa rader i MySQL eller MariaDB. Fungerar på samma sätt som Postgres — håll en databas i synk med ett kalkylblad, läs in en städad fil i den, eller hämta en referenstabell till dina flöden.",
  },
  "mysql.tagline": {
    en: "96068a36",
    sv: "Läs och skriv i din MySQL-databas från ett flöde.",
  },
  "mysql.technical_notes": {
    en: "524aef26",
    sv: "Delar kontraktet med rader + rubriker med Sheets-, Excel- och Postgres-stegen, så samma ETL-flöde kan riktas mot MySQL med en stegändring. Anslutningspool med *sql.DB och lat utrensning av inaktiva anslutningar. Upsert-steget rapporterar antal infogade och uppdaterade separat via ROW_COUNT()-semantik, så senare aviseringar kan säga 'X nya + Y uppdaterade' i stället för en enda summa.",
  },
  "notion.description": {
    en: "d608ef07",
    sv: "Skapa sidor och sök i databaser. Spegla Notion-innehåll till en databas för analys, reagera på nya poster genom pollning, eller skriv strukturerad data från ett flöde till en projekttavla utan att någon behöver lämna Notion.",
  },
  "notion.tagline": {
    en: "c60d7fee",
    sv: "Håll sidor och databaser aktuella utan klipp och klistra.",
  },
  "notion.technical_notes": {
    en: "963a682c",
    sv: "OAuth + Notions API. Notion-Version är låst till 2022-06-28 så att beteendet är stabilt mellan installationer. Mönstret 'utlös vid ny databasrad' byggs av poll_trigger + notion_query_database + secret_set — samma markörbaserade dubblettrensning som Gmail använder; ingen egen triggerdrop behövs.",
  },
  "nshift.description": {
    en: "fab8dbd0",
    sv: "Boka paketförsändelser hos dina transportörer och få tillbaka spårningsnumren. nShift (tidigare Unifaun/Consignor) ligger framför transportörerna — PostNord, DHL, Bring, Schenker och de övriga — så en anslutning täcker allihop. Det naturliga flödet är: en order markeras som skickad, försändelsen bokas, och sedan får kunden spårningslänken via sms eller e-post. Du kan också slå upp en försändelse igen, eller radera en som blivit felbokad.",
  },
  "nshift.tagline": {
    en: "f9e103e3",
    sv: "Boka frakter, och följ paketen på vägen.",
  },
  "nshift.technical_notes": {
    en: "e6ce64aa",
    sv: "Autentiseras med din nShift-API-nyckel (Bearer), som anges en gång som nShift-anslutningen på den här sidan (lagras krypterat som conn.nshift.*) och matas in vid körning — inga uppgifter på steget eller i flödet. Anslutningen väljer även miljö: den står som standard på **integration**, nShifts sandlåda, så att ett halvfärdigt flöde inte kan boka en riktig, fakturerbar försändelse; byt till produktion när du är redo. Bygger på ExtAPI (POST /rs-extapi/v1/shipments med flera). Shipment-indata är nShifts eget shipment-objekt — avsändare, mottagare, kolli, tjänst — som vanligtvis byggs per order av ett tidigare steg. Bokning kostar pengar och nShift har ingen idempotensnyckel, så skapa-steget gör aldrig automatiska omförsök och motorn rensar dubbletter i återupptagna körningar; spårningsnumren kommer ut kommaseparerade på en egen port.",
  },
  "pdf.description": {
    en: "ab0adf28",
    sv: "Arbeta med själva PDF-filerna — slå ihop flera till en, dela en i flera, läs hur många sidor den har. Lokala operationer på filer som redan finns i din arbetsyta: inget konto att ansluta, inga uppgifter, och inget lämnar maskinen. Den naturliga avslutningen på ett arkiveringsflöde är en fil till revisorn i stället för fyrtio, och det är det här.",
  },
  "pdf.tagline": {
    en: "fc3c8360",
    sv: "Skapa en PDF, eller plocka ut texten och sidorna ur en.",
  },
  "pdf.technical_notes": {
    en: "b2d40de6",
    sv: "Bygger på pdfcpu, i processen och i ren Go — ingen extern binär, ingen webbläsarmotor, och omkring 3 MB på daemon-binären. Medvetet INTE textextraktion: pdfcpu gör inte det, och alternativen i ren Go klarar enkla dokument med textlager och faller på en verklig faktura med tabell och inbäddade typsnitt. För att läsa vad en PDF SÄGER, koppla den till ingången Filer på ett AI-steg i stället — modellen läser de renderade sidorna, så en skanning fungerar lika bra som en text-PDF. Varje steg här kontrollerar %PDF-huvudet innan byten lämnas till parsern, så en felkopplad fil rapporterar sig själv i stället för att dyka upp som ett fel om ett trasigt dokument. Delning är begränsad till 200 delar och varje steg till 32 MiB indata, så ett felkopplat steg kan inte fylla körningens skrivutrymme.",
  },
  "roaring.description": {
    en: "88cc9b65",
    sv: "Slå upp ett företag på organisationsnummer och få tillbaka vilka de faktiskt är — registrerat namn, status, adress och skatteuppgifter. Vardagsnyttan är att berika en lead eller en order: ett formulär ger dig ett organisationsnummer, det här gör det till en riktig företagspost du kan lägga i CRM:et, eller kontrollera statusen på innan du ger kredit. Har du bara ett namn söker du först för att hitta organisationsnumret, och berikar sedan varje träff.",
  },
  "roaring.tagline": {
    en: "e6dff91b",
    sv: "Slå upp svenska företag och deras officiella uppgifter.",
  },
  "roaring.technical_notes": {
    en: "cea1f280",
    sv: "Autentiseras med din Consumer Key och Consumer Secret från Roaring, som anges en gång som Roaring-anslutningen på den här sidan (lagras krypterat som conn.roaring.*) och växlas mot en OAuth2-åtkomsttoken vid körning — token cachas tills strax innan den går ut, så en For-each över många företag inte autentiserar om per rad. Bygger på Roarings företagsändpunkter (GET /{country}/company/overview/{version}/{orgnr} samt företagssökningen). Standard är Sverige ('se'); sätt 'country' för en annan nordisk marknad som Roaring täcker. Båda stegen är läsningar och kan därför göra säkra omförsök.",
  },
  "ntfy.description": {
    en: "c0599cd8",
    sv: "Push-notiser till din telefon via ntfy.sh eller en egen ntfy-server. Snabbt att koppla upp — ingen app att installera, du prenumererar bara på ett ämne — så det passar utmärkt för driftaviseringar som snabbt måste nå någon.",
  },
  "ntfy.tagline": {
    en: "7e39fe73",
    sv: "Skicka en pushnotis direkt till din telefon.",
  },
  "ollama.description": {
    en: "9d016eb4",
    sv: "Kör prompter genom en modell på hårdvara du själv styr över — din egen dator eller en server du driftar — i stället för ett molnkonto. Använd den på samma sätt som Claude eller ChatGPT: sammanfatta text, klassificera indata, extrahera fält, skriv utkast till svar. Ta den när texten inte bör lämna din infrastruktur, eller när du hellre slipper betala per anrop.",
  },
  "ollama.tagline": {
    en: "b81c27c6",
    sv: "Kör en AI-modell på din egen dator, där inget lämnar den.",
  },
  "ollama.technical_notes": {
    en: "220ac7cd",
    sv: "Ollamas OpenAI-kompatibla chattändpunkt. Server-URL:en sitter på den här anslutningen (standard http://localhost:11434); en API-nyckel är valfri och behövs bara om din instans står bakom en proxy som kräver autentisering. Modellerna är de du har hämtat hem, så modellfältet är fritext i stället för en lista. Extrahera fält och Klassificera kräver en modell som klarar tool-calls (llama3.1, qwen2.5, mistral-nemo och liknande) — om en modell ignorerar det påtvingade verktygsanropet faller steget tillbaka på att läsa JSON ur svaret. En server på localhost kräver dessutom att DAZYFLOW_ALLOW_PRIVATE_EGRESS är satt på daemonen, eftersom SSRF-skyddet blockerar privata adresser som standard.",
  },
  "open-meteo.description": {
    en: "1d59e78f",
    sv: 'Läs vädret för vilken punkt som helst på kartan — kostnadsfritt för privat, icke-kommersiell användning, utan konto och API-nyckel. Ge ett steg en koordinat — skriven, eller inkopplad från en geokodning, ett formulärfält eller en enhets GPS — och få aktuellt väderläge (en sammanfattning på en rad, temperaturen och ett Clear/Rain/Snow-ord du kan förgrena på) eller en flerdygnsprognos. Bygg ett flöde som "sms:a mig om det regnar i morgon", en morgonbriefing eller en frostvarning för växthuset. För kommersiell användning lägger du till en API-nyckel, varpå den växlar till Open-Meteos betalda ändpunkt.',
  },
  "open-meteo.tagline": {
    en: "0c16c075",
    sv: "Fria väderprognoser och historik, utan konto.",
  },
  "open-meteo.technical_notes": {
    en: "83b34d31",
    sv: "Bygger på Open-Meteos Forecast API (GET /v1/forecast) — aktuellt väderläge väljer current=-fälten, och prognosdroppen läser kolumnarrayerna i daily= (weather_code, temperature_2m_max/min, precipitation_probability_max) med forecast_days och timezone=auto för lokala dygn. Värdena i weather_code är WMO-koder som mappas till beskrivningar och ett förgreningsbart klassord. Den kostnadsfria icke-kommersiella värden (api.open-meteo.com) behöver ingen nyckel; anger du den valfria API-nyckeln per organisation går förfrågningarna till den kommersiella värden (customer-api.open-meteo.com) med en apikey-parameter. Att avgöra om din användning är kommersiell — och att ange en nyckel när den är det — är ditt ansvar. Enheter kan vara metric (°C, m/s) eller imperial (°F, mph).",
  },
  "openstreetmap.description": {
    en: "aee7952b",
    sv: "Arbeta med platser och koordinater. Med Plats-steget väljer du en punkt på en karta (eller slår upp en stad/adress) och skickar ut dess koordinat; Slå upp en plats gör en koordinat till ett platsnamn. Passar naturligt ihop med OpenWeather — välj eller slå upp ett ställe och koppla sedan koordinaten till en väderuppslagning.",
  },
  "openstreetmap.tagline": {
    en: "2739d559",
    sv: "Gör en adress till en plats, och en plats till en adress.",
  },
  "openstreetmap.technical_notes": {
    en: "699ee292",
    sv: "Båda stegen visar en OpenStreetMap-kartväljare på kortet (slå av den med 'Visa karta på kortet' för ett smalare steg). Plats geokodar en skriven eller inkopplad plats; Slå upp en plats bakåtgeokodar den valda eller inkopplade koordinaten. Geokodningen anropar OpenStreetMaps Nominatim-tjänst vid körning genom den SSRF-skyddade HTTP-klienten med en identifierande User-Agent — inget konto och ingen nyckel. Nominatims publika tjänst är hastighetsbegränsad (~1 förfrågan/sekund); för tyngre användning kör du en egen och sätter DAZYFLOW_NOMINATIM_URL. © OpenStreetMap contributors.",
  },
  "openweather.description": {
    en: "61bbf141",
    sv: 'Läs vädret för vilken punkt som helst på kartan. Ge ett steg en koordinat — skriven, eller inkopplad från en geokodning, ett formulärfält eller en enhets GPS — och få aktuellt väderläge (en sammanfattning på en rad, temperaturen och ett Clear/Rain/Snow-ord du kan förgrena på) eller en 5-dygnsprognos. Bygg ett flöde som "sms:a mig om det regnar i morgon", en morgonbriefing eller en frostvarning för växthuset.',
  },
  "openweather.tagline": {
    en: "6b895a74",
    sv: "Vädret just nu och prognosen, var som helst i världen.",
  },
  "openweather.technical_notes": {
    en: "ca2d1566",
    sv: "Bygger på OpenWeathers kostnadsfria ändpunkter — Current Weather (GET data/2.5/weather) och 5-dygnsprognosen i 3-timmarssteg (GET data/2.5/forecast), som prognosdroppen sammanställer till min/max + väderläge per dag. Fungerar med vilken vanlig API-nyckel som helst på den kostnadsfria planen — ingen betald 'One Call by Call'-prenumeration behövs. Autentiseras med din API-nyckel (appid), som lagras en gång på integrationssidan som en anslutning per organisation och matas in vid körning — ingen nyckel på steget. Enheter kan vara metric (°C, m/s), imperial (°F, mph) eller standard (K, m/s).",
  },
  "postgres.description": {
    en: "9bfec398",
    sv: "Infoga, uppdatera och läsa rader i en Postgres-databas. Kombinera det med Sheets-, Excel- eller webhook-stegen för att hålla databasen i synk med den källa ditt team utgår från.",
  },
  "postgres.tagline": {
    en: "2733ae53",
    sv: "Läs och skriv i din Postgres-databas från ett flöde.",
  },
  "postgres.technical_notes": {
    en: "211c32d2",
    sv: "Anslutningsregister med pgxpool per (organisation, DSN) och lat utrensning av inaktiva anslutningar. Skicka DSN:en via ${secret.postgres_dsn} från det krypterade hemlighetslagret i stället för att bädda in den i flödets JSON; steget secret_set kan rotera den utan att flödena ändras.",
  },
  "sftp.description": {
    en: "39487629",
    sv: "Flytta filer till och från en filserver (SFTP). Så här fungerar fortfarande mycket i affärslivet: en leverantör eller en bank lämnar en fil över natten, och något måste hämta den, läsa den och agera på den.",
  },
  "sftp.tagline": {
    en: "1dd0c261",
    sv: "Flytta filer till och från en server ditt team redan använder.",
  },
  "sftp.technical_notes": {
    en: "439522e1",
    sv: "Servern — adress, port, användarnamn och antingen ett lösenord eller en privat SSH-nyckel — ställs in en gång här och matas in i varje SFTP-steg vid körning; lösenordet och nyckeln förvaras i det krypterade hemlighetslagret. Verifiering av värdnyckeln har ingen standard och går inte att stänga av: klistra in serverns ”SHA256:…”-fingeravtryck (eller en known_hosts-rad), och tills du gör det misslyckas Testa anslutning med det fingeravtryck servern faktiskt erbjöd, så att du kan stämma av det och kopiera in det. Att acceptera vilken nyckel som helst skulle göra en tyst man-in-the-middle möjlig, och det är inloggningsuppgifterna den skulle samla in. ”Bara nya sedan förra körningen” på Lista filer håller reda på den senaste ändringstiden plus namnen som delar den sekunden, så ett flöde som släpper tjugo filer inom en sekund inte tappar eftersläntrarna. En server per anslutning här, men ett steg är inte längre begränsat till den: sparade servrar på sidan Servrar är namngivna, det får finnas hur många som helst, och ett SFTP-steg väljer en med namn. Den här anslutningen är fortfarande vad ett steg använder när det inte namnger någon, så flöden byggda innan de fanns fungerar vidare oförändrat.",
  },
  "ssh.tagline": {
    en: "999a0949",
    sv: "Kör ett kommando på en av dina egna servrar.",
  },
  "ssh.description": {
    en: "69dad2de",
    sv: "Kör ett kommando på en server över SSH och gå vidare med det den skrev ut — starta om en tjänst, ta en säkerhetskopia, läs en logg, fråga hur fulla diskarna är. Det når allt som har en sshd och kräver inget installerat på maskinen, vilket är det som gör det till steget som fungerar på en apparat eller en burk du aldrig kommer få köra en agent på.",
  },
  "ssh.technical_notes": {
    en: "b3c82b82",
    sv: "Servrar namnges och ställs in en gång på sidan Servrar — adress, port, användarnamn och antingen ett lösenord eller en privat SSH-nyckel, förvarade i det krypterade hemlighetslagret — och ett steg väljer en med namn, så inget hamnar i ett flöde. SFTP-stegen väljer ur samma lista: det är samma maskin, samma inloggning och samma värdnyckel, och paret som visar det är att dumpa en databas över SSH och hämta filen över SFTP. Verifiering av värdnyckeln har ingen standard och går inte att stänga av: tills du klistrar in serverns ”SHA256:…”-fingeravtryck (eller en known_hosts-rad) misslyckas Testa anslutning med det fingeravtryck servern faktiskt erbjöd, så att du kan stämma av det och kopiera in det. Ingen TTY tilldelas, så ett kommando som stannar och frågar något — vanliga `sudo`, till exempel — väntar tills stegets tidsgräns i stället för att få svar; använd `sudo -n` med en NOPASSWD-regel. Miljövärden exporteras i skalet i stället för att skickas som SSH:s env-begäran, som en sshd med tom AcceptEnv tyst skulle kasta. En avslutningskod som inte är noll gör att steget misslyckas om du inte ber det fortsätta och förgrena på koden i stället, och varje utdataström har ett tak så att ett skenande kommando inte kan fylla körningsposten.",
  },
  "slack.description": {
    en: "5ab3ab5c",
    sv: "Skicka meddelanden från dina flöden, och starta flöden när någon @-nämner din bot. Anslut en arbetsyta en gång och din bot kan lägga upp meddelanden i alla kanaler den är medlem i — praktiskt för aviseringar, dagliga rapporter eller enkla bottar som gör chattmeddelanden till handling.",
  },
  "slack.tagline": {
    en: "03b70388",
    sv: "Skriv i teamets kanaler, och låt ett meddelande sätta igång arbetet.",
  },
  "slack.technical_notes": {
    en: "5761fe68",
    sv: "OAuth 2.0 med scopen chat:write, channels:read och channels:history. Triggern slack_on_mention använder Slacks Events API — peka din Slack-apps Event Subscription-URL på /api/v1/events/slack/<tenant> och sätt DAZYFLOW_SLACK_SIGNING_SECRET på daemonen (HMAC-SHA256-signaturverifiering + 5 minuters replayfönster).",
  },
  "smhi.description": {
    en: "39c4455a",
    sv: "Kostnadsfritt nordiskt väder från Sveriges meteorologiska institut — inget konto och ingen API-nyckel. Ge SMHI Väder-stegen en koordinat (välj den med ett Plats-steg) och få aktuellt väderläge eller en flerdygnsprognos för vilken punkt som helst i Norden och området omkring, i metriska enheter.",
  },
  "smhi.tagline": {
    en: "4a7ccd74",
    sv: "Officiella svenska prognoser och varningar, direkt från SMHI.",
  },
  "smhi.technical_notes": {
    en: "60c57f10",
    sv: "Bygger på SMHI:s Open Data-prognos-API (punktändpunkten i snow1g v1: GET …/geotype/point/lon/{lon}/lat/{lat}/data.json?parameters=…) — utan nyckel. Alltid metriskt (°C, m/s); värdena i symbol_code (Wsymb2 1–27) mappas till beskrivningar, och prognosdroppen sammanställer stegen under dygnet till min/max + väderläge per dag (UTC-dygn). Täckningen är SMHI:s modellområde — en punkt utanför det ger 'out of bounds'.",
  },
  "sqlite.description": {
    en: "79207676",
    sv: "Infoga, uppdatera och läsa rader i en SQLite-fil i din arbetsyta. Passar bra för tillfälliga databaser per organisation, för att prototypa flöden innan du sätter upp en riktig databas, eller för att hålla en liten referenstabell intill dina andra filer i arbetsytan.",
  },
  "sqlite.tagline": {
    en: "079efa38",
    sv: "En liten databas i en fil, för listorna dina flöden håller.",
  },
  "sqlite.technical_notes": {
    en: "f9b435a6",
    sv: "Ingen anslutningspool — att öppna filen tar mikrosekunder, så en ny handtag per anrop går bra. .sqlite-filen ligger i arbetsytans sandlåda som alla andra filer där; sandlådans regler gäller.",
  },
  "knowledge.description": {
    en: "3607c326",
    sv: "Lagra dokument som avsnitt ett flöde kan söka i på betydelse snarare än på nyckelord, och lämna sedan de närmaste till ett AI-steg att svara utifrån — hämtningshalvan av att ställa en fråga till din egen handbok, dina priser eller dina policyer. Anslut en gång för att säga vem som gör text till inbäddningar: ChatGPT, Gemini eller Ollama på din egen maskin. Avsnitten ligger i den här arbetsytan, bredvid dina Samlingar.",
  },
  "knowledge.tagline": {
    en: "4691a09b",
    sv: "Låt dina flöden svara utifrån dina egna dokument.",
  },
  "mcp.description": {
    en: "1a4554a8",
    sv: "Steg som kommer från en MCP-server som din organisation har lagt till, i stället för från en koppling som vi har skrivit. Peka Dazyflow mot en servers adress under Admin → MCP-servrar, så dyker varje verktyg den publicerar upp här som ett steg — inget att installera, och ingen koppling att vänta på. Servern har sin egen inloggning, så dessa steg behöver ingen separat anslutning.",
  },
  "mcp.tagline": {
    en: "639b7ebd",
    sv: "Ta in verktyg från vilken MCP-server du än kopplar in.",
  },
  "mcp.technical_notes": {
    en: "ec20240d",
    sv: 'Verktygen läses vid handskakningen över streamable HTTP (MCP-revision 2025-11-25) och blir steg med id:t mcp:<server>:<verktyg>; ett verktygs argument blir stegets pinnar. Varje server tillhör den organisation som registrerade den och kan bara nås av den organisationens flöden. En server som slutar svara behåller sina steg beskrivna — de visar en "Kräver anslutning"-banner och vägrar att köra — så flöden som använder dem behåller sina kopplingar.',
  },
  "standard-library.description": {
    en: "086c5d7b",
    sv: "Allt som inte är en särskild app: delarna du fogar ihop apparna med. Skicka ett flöde längs den ena vägen eller den andra, upprepa ett steg för varje rad, pausa för att någon ska godkänna, vänta en stund, läs och skriv filer, städa upp en lista (sortera den, ta bort dubbletter, gruppera den, räkna ihop), läs och skriv din egen databas, och starta ett flöde enligt schema eller när något hör av sig.",
  },
  "standard-library.tagline": {
    en: "df142efe",
    sv: "Vardagsbyggstenarna som varje flöde är gjort av.",
  },
  "stripe.description": {
    en: "44844cdb",
    sv: "Reagera på betalningar i samma stund de sker — lyckade, misslyckade eller en avslutad prenumeration — och gör något med dem: skapa en kund, mejla en faktura, dela ut en betallänk eller gör en återbetalning. Bygg ett påminnelseflöde som jagar en misslyckad betalning, en välkomstserie vid en kunds första betalning, eller en direktavisering när någon säger upp sig.",
  },
  "stripe.tagline": {
    en: "c21bc41b",
    sv: "Följ betalningar, kunder och prenumerationer medan de sker.",
  },
  "stripe.technical_notes": {
    en: "925a9244",
    sv: "Åtgärderna autentiseras med din hemliga Stripe-nyckel, som anges en gång som Stripe-anslutningen på den här sidan (lagras krypterad som conn.stripe.api_key) och matas in vid körning — ingen nyckel på steget eller i flödet. Triggrarna för betalning, misslyckad betalning och avslutad prenumeration är Stripe-webhooks: peka en ändpunkt på /api/v1/events/stripe/<tenant>, prenumerera på motsvarande händelser (payment_intent.succeeded, payment_intent.payment_failed, customer.subscription.deleted) och spara ändpunktens signeringshemlighet (whsec_…) som STRIPE_WEBHOOK_SECRET — varje leverans Stripe-Signature verifieras mot den. Föredrar du pollning framför webhooks? Bygg Schema → Lista händelser i stället.",
  },
  "ticketmaster.description": {
    en: "23474d45",
    sv: "Sök efter liveevenemang — konserter, matcher, föreställningar — på artist, stad och datum, och få en prydlig rad var med lokalen, datumet och en länk till biljetterna. Eller bevaka en sökning och låt flödet säga till när något nytt annonseras: ett turnédatum, en extrakväll, ett förband. Kombinera med Spotify för att göra artisterna någon följer till en konsertlista som håller sig uppdaterad av sig själv.",
  },
  "ticketmaster.tagline": {
    en: "c1b47d3e",
    sv: "Hitta evenemang och konserter nära dig, så fort biljetterna släpps.",
  },
  "ticketmaster.technical_notes": {
    en: "45d86077",
    sv: "Ticketmasters Discovery API v2, autentiserat med Consumer Key för en app skapad på developer.ticketmaster.com. Nyckeln skickas som frågeparameter i stället för som rubrik, vilket är Ticketmasters egen utformning, så inget felmeddelande från de här stegen återger någonsin anrops-URL:en. Den kostnadsfria nivån är 5000 anrop per dygn och 5 per sekund, delade av alla flöden som använder nyckeln — därför kontrollerar bevakningssteget en gång om dygnet som standard, och därför byggs bevakning av en lång artistlista hellre som Schema → För varje → Sök evenemang än som många bevakningssteg.\n\nTäckningen är Ticketmasters eget utbud — Ticketmaster, TicketWeb, Universe, Frontgate och andrahandsförsäljning. Stark i Norden, där Ticketmaster säljer merparten av arena- och festivalbiljetterna; en klubbspelning som säljs via DICE, Tickster eller Billetto dyker inte upp, och ingen sökning får den att göra det.\n\nDet finns inga webhooks, så bevakningssteget pollar och kommer ihåg de evenemangs-id:n det redan rapporterat (per flöde och steg, i den krypterade markörlagringen, begränsat till de senaste 1000). Den första kontrollen efter publicering registrerar vad som redan är släppt utan att utlösa, så att slå på en bevakning annonserar inte hundra evenemang som redan fanns.",
  },
  "twilio.description": {
    en: "43d2e415",
    sv: 'Skicka SMS till vilken telefon som helst, direkt från ett flöde. Ta det när en avisering behöver landa i någons ficka — ett "ordern är skickad" eller en tidspåminnelse till en kund, en verifieringskod, ett jourlarm, eller ett tips i samma stund en trigger utlöses.',
  },
  "twilio.tagline": {
    en: "0db066b3",
    sv: "Skicka ett sms var som helst i världen.",
  },
  "twilio.technical_notes": {
    en: "788efd83",
    sv: "Autentiseras med ditt Twilio Account SID och Auth Token, som anges en gång som Twilio-anslutningen på den här sidan (lagras krypterat som conn.twilio.*) och matas in vid körning — inga uppgifter på steget eller i flödet. Skickar via Twilios Messages API; 'Från' måste vara ett av dina Twilio-nummer i E.164 (+15551234567), eller så anger du ett Messaging Service SID (MG…) i stället.",
  },
  "webhook.description": {
    en: "81dfc981",
    sv: "Pinga ett annat system i samma stund som något händer — en kanal i Slack eller Discord, Teams, PagerDuty, eller en egen mottagare. Det enklaste sättet att berätta för något annat att ett flöde kommit någonstans.",
  },
  "webhook.tagline": {
    en: "5b0fd042",
    sv: "Berätta för en annan tjänst att något hänt, i samma stund det sker.",
  },
};

// The app's own NAME, where it is generic English rather than a product name.
//
// Two populations, one map, because the key is the English either way: the
// display names on the Apps pages ("Mailbox (IMAP)") and the shorter
// Integration a manifest carries ("Calendar"), which the palette and the
// Inspector show. Brands are absent on purpose — Slack stays Slack, and so do
// SFTP, PDF and HTTP. "Standard library" and "MCP servers" are absent for a
// different reason: the Apps page already renders those two through i18n keys
// (integrations.builtinGroup / mcpGroup), and a second translation here would
// be free to drift from them.
export const SV_INTEGRATION_NAMES: Record<string, string> = {
  Knowledge: "Kunskap",
  Calendar: "Kalender",
  "Calendar (any provider)": "Kalender (valfri leverantör)",
  Collections: "Samlingar",
  Email: "E-post",
  "FTP server": "FTP-server",
  "SSH server": "SSH-server",
  Mailbox: "Brevlåda",
  "Read email": "Läs mejl",
  "Send a webhook": "Skicka en webhook",
  "Send email": "Skicka mejl",
  "Web request": "Webbanrop",
};
