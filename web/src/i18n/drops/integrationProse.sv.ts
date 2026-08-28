// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

import type { DescriptionMap } from "../../lib/dropText";

// Swedish prose for the Apps pages: each integration's friendly description
// and its collapsible technical notes.
//
// Keys are '<slug>.description' and '<slug>.technical_notes'. `en` is the
// descriptionFingerprint of the English
// in integrationMeta.ts that each translation was made from — same drift guard
// as the drop descriptions: edit the English there and the reader falls back
// to it instead of reading a stale Swedish paragraph. That matters more here
// than elsewhere, because this English lives in the frontend and changes
// without a catalog rebuild.
//
// Product names, env vars, scopes, endpoints and header names stay English:
// they are what the reader will type into a dashboard or grep for in a log.
export const SV_INTEGRATION_PROSE: DescriptionMap = {
  "46elks.description": {
    en: "9bc17c9e",
    sv: "Skicka SMS direkt från ett flöde via 46elks, en svensk meddelandeleverantör som är populär i hela Norden. Skicka från ett alfanumeriskt avsändarnamn (som \"Acme\") för envägsaviseringar — orderuppdateringar, påminnelser, verifieringskoder — eller från ett av dina 46elks-nummer när du vill att mottagaren ska kunna svara. Med en testkörningsknapp kan du validera ett meddelande utan att skicka det eller bli fakturerad.",
  },
  "46elks.technical_notes": {
    en: "9005d254",
    sv: "Autentiseras med ditt 46elks-API-användarnamn och lösenord (HTTP Basic), som anges en gång som 46elks-anslutningen på den här sidan (lagras krypterat som conn.46elks.*) och matas in vid körning — inga uppgifter på steget eller i flödet. Skickar en formulärkodad POST till 46elks ändpunkt /a1/sms. 'Från' är antingen E.164 (går att svara på) eller ett alfanumeriskt avsändar-ID (max 11 tecken, måste innehålla en bokstav, går inte att svara på). 46elks har ingen idempotensnyckel, så steget gör aldrig automatiska omförsök och motorn rensar dubbletter i återupptagna körningar — ett nytt utskick skulle faktureras dubbelt.",
  },
  "chatgpt.description": {
    en: "c65b2822",
    sv: "Kör prompter genom ChatGPT, OpenAI:s AI-assistent. Använd den på samma sätt som Claude — sammanfatta text, klassificera indata, extrahera fält eller skriv utkast till svar — där du hellre använder en OpenAI-modell.",
  },
  "chatgpt.technical_notes": {
    en: "455a4aed",
    sv: "OpenAI:s Chat Completions API, autentiserat med API-nyckeln som är satt på den här anslutningen — flödena hämtar den automatiskt, ingen nyckel på steget. De strukturerade stegen (Extrahera fält, Klassificera) använder OpenAI:s function tool-calls.",
  },
  "claude.description": {
    en: "2a278926",
    sv: "Kör prompter genom Claude, Anthropics AI-assistent. Användbart för att sammanfatta text från tidigare steg, klassificera indata, generera svar, eller var som helst i flödet där du vill ha en språkmodell med i loopen.",
  },
  "claude.technical_notes": {
    en: "2ac221d3",
    sv: "Autentiseras med API-nyckeln som är satt på den här anslutningen — flödena hämtar den automatiskt, ingen nyckel på steget. För lokal utveckling utan nyckel dirigerar flaggan dzd --claude-cli anropen genom en lokal `claude -p`-CLI plus en MCP-server, så att flöden kan köra chattvägen mot din redan inloggade CLI.",
  },
  "collections.description": {
    en: "17018173",
    sv: "Spara rader i en inbyggd samling utan någon uppsättning, och läs dem tillbaka — det är lagringen bakom Samlingar-sidan i appen. Ta det för att samla ett flödes utdata för granskning, bygga en enkel instrumentpanel, eller hålla löpande summor utan att sätta upp en riktig databas.",
  },
  "discord.description": {
    en: "acd35112",
    sv: "Lägg upp meddelanden i en Discord-kanal från ett flöde — en ping när driftsättningen är klar, en avisering när bygget går sönder, en daglig sammanfattning, eller ett tips till teamet i samma stund något händer. Sätt avsändarnamn och avatar per meddelande om du vill.",
  },
  "discord.technical_notes": {
    en: "f4c9e119",
    sv: "Lägger upp via en webhook-URL till en Discord-kanal, som anges en gång som Discord-anslutningen på den här sidan (lagras krypterad som conn.discord.webhook_url) — ingen bot och ingen OAuth-app behövs. Skapa den under Serverinställningar → Integrationer → Webhooks. Valfria överskrivningar av användarnamn och avatar per meddelande.",
  },
  "email.description": {
    en: "e7e1859a",
    sv: "Skicka e-post via en SMTP-server som du ställer in. Välj det här när du har en delad brevlåda eller en transaktionsleverantör med SMTP-relä (SendGrid, SES, Postmark) och hellre konfigurerar en server än går igenom OAuth.",
  },
  "email.technical_notes": {
    en: "3492a61e",
    sv: "E-postservern — värd, port, säkerhet (STARTTLS på 587 / implicit TLS på 465 / ingen), användarnamn, lösenord och Från-adress — konfigureras en gång här och matas in i varje E-post-steg vid körning; lösenordet ligger i det krypterade hemlighetslagret. Använd 'Testa anslutningen' för att bekräfta server och inloggning innan du sparar.",
  },
  "excel.description": {
    en: "b17f00b1",
    sv: "Läs in .xlsx-arbetsböcker som rader, och skriv rader tillbaka som en ny arbetsbok. Användbart när någon lägger en fil i arbetsytan och du vill städa den, koppla den mot en referenstabell eller läsa in den i en riktig databas.",
  },
  "excel.technical_notes": {
    en: "7a2f56f6",
    sv: "Bygger på biblioteket excelize. Kontraktet med rader + rubriker matchar Sheets och databasdropparna, så en Excel-fil kan matas direkt in i en Postgres-upsert med ett map_rows emellan.",
  },
  "fortnox.description": {
    en: "703b2c13",
    sv: "Hantera kunder och fakturor i Fortnox, Sveriges ledande bokföringsplattform för småföretag. Skapa en kund från en anmälan, ställ ut en faktura till den, och välj vem som ska faktureras i en sökbar lista över dina befintliga kunder. Polla fakturor på status för att bygga ett flöde som reagerar på nyligen betalda fakturor — ett tackmejl, ett leveranssteg — eller påminner om förfallna.",
  },
  "fortnox.technical_notes": {
    en: "0f749f7f",
    sv: "Fortnox OAuth 2.0 (authorize hos apps.fortnox.se/oauth-v1) med scope per resurs — customer, invoice och companyinformation täcker de steg som finns. Token-ändpunkten använder client_secret_basic (uppgifterna i en HTTP Basic-rubrik), och refresh-tokens roterar vid varje förnyelse; daemonen sparar den roterade token och förnyar vid utgång, så långlivade flöden fortsätter fungera — men ett konto som stått stilla längre än Fortnox fönster för refresh-tokens (~31 dagar) måste anslutas om. Anrop och svar använder Fortnox singulara PascalCase-hölje ({\"Customer\":…}, {\"Invoice\":…}). Fortnox har ingen idempotensnyckel, så skapa-stegen gör inga automatiska omförsök (ett omförsök skulle ge dubbletter); och inga webhooks, så 'utlös vid betald faktura' byggs som Schema → Lista fakturor (filter=fullypaid) → För varje → dubblettrensning på DocumentNumber.",
  },
  "git.description": {
    en: "c69a927c",
    sv: "Klona repon och checka ut grenar inne i din arbetsyta. Ta det när ett flöde behöver granska källkod, hämta mallar från ett känt repo, eller lägga upp filer innan ett annat steg arbetar på dem.",
  },
  "git.technical_notes": {
    en: "2959c2a2",
    sv: "Alla operationer hålls inne i arbetsytans sandlåda via normalisering av sökvägar — kloner skriver in i sandlådans rot, aldrig ovanför. Skrivskyddat i dag; skrivoperationer mot fjärrepon stöds inte.",
  },
  "github.description": {
    en: "3c48c703",
    sv: "Skapa ärenden, kommentera befintliga och starta flöden vid push eller ny PR. Vanliga mönster: dirigera en inkommande avisering till ett spårat ärende, lägg upp en driftsättningsavisering när commits landar på main, starta ett triage-flöde när någon öppnar en PR.",
  },
  "github.technical_notes": {
    en: "8e75840a",
    sv: "Personliga åtkomsttokens eller OAuth-användartokens via Authorization: Bearer. Webhook-triggrarna använder GitHubs HMAC-schema X-Hub-Signature-256 — peka repots webhook-URL på /api/v1/events/github/<tenant>, med webhookens Secret lika med DAZYFLOW_GITHUB_WEBHOOK_SECRET. API-versionen är låst till 2022-11-28.",
  },
  "gmail.description": {
    en: "3a789bcd",
    sv: "Skicka mejl, sök i din inkorg och läs hela meddelandetexter. Det klassiska användningsfallet: reagera på inkommande mejl när de kommer — kombinera sök-steget med en pollningstrigger, så minns flödet vilka meddelanden det redan bearbetat och gör inte om samma arbete.",
  },
  "gmail.technical_notes": {
    en: "2fb6701f",
    sv: "Gmail API + Google OAuth. access_type=offline + prompt=consent följer med vid authorize så att refresh_token finns kvar mellan körningar. Markörens dubblettrensning lagras i det krypterade hemlighetslagret via secret_set + ${secret.…}-substitution och överlever omstarter av daemonen.",
  },
  "google-calendar.description": {
    en: "fc0aa518",
    sv: "Skapa kalenderhändelser och lista vad som är på gång. Lägg in ett möte i en kalender när ett flöde utlöses, gör en inkommande bokning till en händelse, eller hämta dagens schema till en morgonsammanfattning.",
  },
  "google-calendar.technical_notes": {
    en: "85ee9aa6",
    sv: "Delar OAuth-klienten 'google' med Gmail, Sheets och Forms — ett medgivande täcker alla, och att ansluta Calendar lägger bara till kalender-scopen. Tider anges som RFC3339 för händelser med klockslag eller som enkla datum för heldagshändelser; återkommande händelser expanderas till enskilda tillfällen i starttidsordning.",
  },
  "google-drive.description": {
    en: "1d269698",
    sv: "Lista, ladda ner och ladda upp filer i Google Drive. Hämta en fil för att mejla den som bilaga, arkivera ett inkommande dokument, plocka ut ett Doc eller Sheet som PDF, eller lägg tillbaka genererade filer i en mapp för ditt team.",
  },
  "google-drive.technical_notes": {
    en: "c14de76f",
    sv: "Delar OAuth-klienten 'google' med Gmail, Sheets och Forms — ett medgivande täcker alla, och att ansluta Drive lägger bara till drive-scopen. Google-dokument (Docs/Sheets/Slides) har inga råa byte, så nedladdningsdroppen exporterar dem till ett konkret format (PDF som standard). Nedladdningar hamnar i körningens tillfälliga utrymme.",
  },
  "google-forms.description": {
    en: "171d447f",
    sv: "Starta ett flöde när ett Google-formulär får nya svar, där varje svar har frågans rubrik som nyckel — koppla det direkt till ett tillägg i Sheets för att logga inskickade svar, eller till vilket steg som helst som tar poster.",
  },
  "google-forms.technical_notes": {
    en: "7a4d6aef",
    sv: "Delar OAuth-klienten 'google' med Gmail och Sheets; inkrementell auktorisering innebär att en anslutning av Forms bara begär forms.*-scopen (svar + formulärets innehåll, skrivskyddat), utan att Gmail/Sheets behöver godkännas igen. Triggern pollar forms.responses.list mot en markör per flöde i det krypterade hemlighetslagret.",
  },
  "google-sheets.description": {
    en: "80bb345b",
    sv: "Läs rader från ett kalkylblad och lägg till rader i det. Använd det för att hålla ett blad i synk med en databas, logga inkommande händelser så att icke-tekniska kollegor kan titta på dem, eller hämta in en referenstabell till andra flöden.",
  },
  "google-sheets.technical_notes": {
    en: "4ed1377c",
    sv: "Delar OAuth-klienten 'google' med Gmail — ett medgivande täcker båda. Formen med rader + rubriker är utbytbar med Excel- och databasdropparna, så ett blad kan matas direkt in i en Postgres-upsert utan mellanliggande omvandlingar.",
  },
  "home-assistant.description": {
    en: "347c7486",
    sv: "Styr ditt smarta hem och reagera på vad det gör. Tänd lampor, lås en dörr, ställ termostaten eller kör en scen — och starta ett flöde automatiskt i samma stund en enhets status ändras, som att en dörr öppnas eller en sensor löser ut.",
  },
  "home-assistant.technical_notes": {
    en: "22c3a543",
    sv: "Pratar med din Home Assistant-instans över dess REST-API, med instansens URL och en långlivad åtkomsttoken (skapa en under Profil → Long-Lived Access Tokens) som konfigureras en gång här. En LAN-adress (homeassistant.local, 192.168.x.x) kräver att daemonens utgående trafik till privata nät är påslagen (DAZYFLOW_ALLOW_PRIVATE_EGRESS).",
  },
  "http.description": {
    en: "1c47f1fa",
    sv: "Gör HTTP-förfrågningar till vilket API som helst. Använd det för tjänster som inte har en egen anslutning här än — fyll i URL:en, ange rubriker om du behöver autentisering, och svarets innehåll kommer ut som stegets utdata.",
  },
  "http.technical_notes": {
    en: "65bd043a",
    sv: "SSRF-skyddet blockerar loopback, RFC1918 och link-local (inklusive AWS instansmetadata på 169.254.169.254). Konfigurerbar gräns för svarsstorlek, filter på statuskod och tidsgräns för förfrågan. JSON-/text-MIME-detektering på svaret.",
  },
  "klarna.description": {
    en: "025ffa4d",
    sv: "Hantera dina Klarna-ordrar direkt från ett flöde. Klarna är den nordiska \"köp nu, betala senare\"-kassan, och det här är baksidan av den: slå upp en order, ta betalningen när varorna skickas (helt eller delvis), och återbetala en retur. Kombinera återbetalningen med ett godkännandesteg för det klassiska flödet \"nicka i Slack, återbetala sedan\", eller kontrollera en orders status innan du agerar på den.",
  },
  "klarna.technical_notes": {
    en: "d1248b5e",
    sv: "Autentiseras med ditt Klarna-API-användarnamn och lösenord (HTTP Basic), som anges en gång som Klarna-anslutningen på den här sidan (lagras krypterat som conn.klarna.*) och matas in vid körning — inga uppgifter på steget eller i flödet. Välj dataregion och miljö som en del av anslutningen (EU / Nordamerika / Oceanien × produktion eller playground), vilket väljer API-värden — den utgår från EU:s playground så att en halvkonfigurerad anslutning inte kan flytta riktiga pengar. Bygger på Klarnas Order Management API (v1): debitering och återbetalning POSTar JSON till /ordermanagement/v1/orders/{id}/captures|refunds och läser det nya id:t ur rubriken Capture-ID / Refund-ID (eller Location). Lämna beloppet tomt för att agera på hela det utestående beloppet (en GET fyller i det); belopp anges i valutans minsta enhet (öre/cent). Klarna har ingen pålitlig idempotensnyckel här, så debitering och återbetalning görs aldrig om automatiskt och motorn rensar dubbletter i återupptagna körningar — en upprepning skulle debitera eller återbetala dubbelt. Inga webhooks, så 'utlös vid ny order' byggs som Schema → Hämta order → förgrena på status.",
  },
  "mqtt.description": {
    en: "525c6395",
    sv: "Publicera meddelanden till en MQTT-mäklare — den lätta ryggraden i de flesta hemautomations- och IoT-uppsättningar. Tänd en smart lampa, skicka ett kommando till en enhet, eller sänd ut en statusuppdatering som allt som prenumererar på ämnet plockar upp.",
  },
  "mqtt.technical_notes": {
    en: "862d4b33",
    sv: "Mäklarens ändpunkt (tcp:// eller ssl://; bara värd:port blir tcp://…:1883) och valfritt användarnamn/lösenord anges en gång som MQTT-anslutningen på den här sidan (lagras krypterat som conn.mqtt.*) och matas in vid körning. Stöder QoS-nivåer och retain-flaggan. Mäklare i privata nät är blockerade om inte operatören tillåter utgående trafik dit (DAZYFLOW_ALLOW_PRIVATE_EGRESS).",
  },
  "mysql.description": {
    en: "bd3fe5c0",
    sv: "Infoga, uppdatera och läsa rader i MySQL eller MariaDB. Fungerar på samma sätt som Postgres — håll en databas i synk med ett kalkylblad, läs in en städad fil i den, eller hämta en referenstabell till dina flöden.",
  },
  "mysql.technical_notes": {
    en: "524aef26",
    sv: "Delar kontraktet med rader + rubriker med Sheets-, Excel- och Postgres-stegen, så samma ETL-flöde kan riktas mot MySQL med en stegändring. Anslutningspool med *sql.DB och lat utrensning av inaktiva anslutningar. Upsert-steget rapporterar antal infogade och uppdaterade separat via ROW_COUNT()-semantik, så senare aviseringar kan säga 'X nya + Y uppdaterade' i stället för en enda summa.",
  },
  "notion.description": {
    en: "d608ef07",
    sv: "Skapa sidor och sök i databaser. Spegla Notion-innehåll till en databas för analys, reagera på nya poster genom pollning, eller skriv strukturerad data från ett flöde till en projekttavla utan att någon behöver lämna Notion.",
  },
  "notion.technical_notes": {
    en: "963a682c",
    sv: "OAuth + Notions API. Notion-Version är låst till 2022-06-28 så att beteendet är stabilt mellan installationer. Mönstret 'utlös vid ny databasrad' byggs av poll_trigger + notion_query_database + secret_set — samma markörbaserade dubblettrensning som Gmail använder; ingen egen triggerdrop behövs.",
  },
  "nshift.description": {
    en: "fab8dbd0",
    sv: "Boka paketförsändelser hos dina transportörer och få tillbaka spårningsnumren. nShift (tidigare Unifaun/Consignor) ligger framför transportörerna — PostNord, DHL, Bring, Schenker och de övriga — så en anslutning täcker allihop. Det naturliga flödet är: en order markeras som skickad, försändelsen bokas, och sedan får kunden spårningslänken via sms eller e-post. Du kan också slå upp en försändelse igen, eller radera en som blivit felbokad.",
  },
  "nshift.technical_notes": {
    en: "e6ce64aa",
    sv: "Autentiseras med din nShift-API-nyckel (Bearer), som anges en gång som nShift-anslutningen på den här sidan (lagras krypterat som conn.nshift.*) och matas in vid körning — inga uppgifter på steget eller i flödet. Anslutningen väljer även miljö: den står som standard på **integration**, nShifts sandlåda, så att ett halvfärdigt flöde inte kan boka en riktig, fakturerbar försändelse; byt till produktion när du är redo. Bygger på ExtAPI (POST /rs-extapi/v1/shipments med flera). Shipment-indata är nShifts eget shipment-objekt — avsändare, mottagare, kolli, tjänst — som vanligtvis byggs per order av ett tidigare steg. Bokning kostar pengar och nShift har ingen idempotensnyckel, så skapa-steget gör aldrig automatiska omförsök och motorn rensar dubbletter i återupptagna körningar; spårningsnumren kommer ut kommaseparerade på en egen port.",
  },
  "roaring.description": {
    en: "88cc9b65",
    sv: "Slå upp ett företag på organisationsnummer och få tillbaka vilka de faktiskt är — registrerat namn, status, adress och skatteuppgifter. Vardagsnyttan är att berika en lead eller en order: ett formulär ger dig ett organisationsnummer, det här gör det till en riktig företagspost du kan lägga i CRM:et, eller kontrollera statusen på innan du ger kredit. Har du bara ett namn söker du först för att hitta organisationsnumret, och berikar sedan varje träff.",
  },
  "roaring.technical_notes": {
    en: "cea1f280",
    sv: "Autentiseras med din Consumer Key och Consumer Secret från Roaring, som anges en gång som Roaring-anslutningen på den här sidan (lagras krypterat som conn.roaring.*) och växlas mot en OAuth2-åtkomsttoken vid körning — token cachas tills strax innan den går ut, så en For-each över många företag inte autentiserar om per rad. Bygger på Roarings företagsändpunkter (GET /{country}/company/overview/{version}/{orgnr} samt företagssökningen). Standard är Sverige ('se'); sätt 'country' för en annan nordisk marknad som Roaring täcker. Båda stegen är läsningar och kan därför göra säkra omförsök.",
  },
  "ntfy.description": {
    en: "c0599cd8",
    sv: "Push-notiser till din telefon via ntfy.sh eller en egen ntfy-server. Snabbt att koppla upp — ingen app att installera, du prenumererar bara på ett ämne — så det passar utmärkt för driftaviseringar som snabbt måste nå någon.",
  },
  "open-meteo.description": {
    en: "1d59e78f",
    sv: "Läs vädret för vilken punkt som helst på kartan — kostnadsfritt för privat, icke-kommersiell användning, utan konto och API-nyckel. Ge ett steg en koordinat — skriven, eller inkopplad från en geokodning, ett formulärfält eller en enhets GPS — och få aktuellt väderläge (en sammanfattning på en rad, temperaturen och ett Clear/Rain/Snow-ord du kan förgrena på) eller en flerdygnsprognos. Bygg ett flöde som \"sms:a mig om det regnar i morgon\", en morgonbriefing eller en frostvarning för växthuset. För kommersiell användning lägger du till en API-nyckel, varpå den växlar till Open-Meteos betalda ändpunkt.",
  },
  "open-meteo.technical_notes": {
    en: "83b34d31",
    sv: "Bygger på Open-Meteos Forecast API (GET /v1/forecast) — aktuellt väderläge väljer current=-fälten, och prognosdroppen läser kolumnarrayerna i daily= (weather_code, temperature_2m_max/min, precipitation_probability_max) med forecast_days och timezone=auto för lokala dygn. Värdena i weather_code är WMO-koder som mappas till beskrivningar och ett förgreningsbart klassord. Den kostnadsfria icke-kommersiella värden (api.open-meteo.com) behöver ingen nyckel; anger du den valfria API-nyckeln per organisation går förfrågningarna till den kommersiella värden (customer-api.open-meteo.com) med en apikey-parameter. Att avgöra om din användning är kommersiell — och att ange en nyckel när den är det — är ditt ansvar. Enheter kan vara metric (°C, m/s) eller imperial (°F, mph).",
  },
  "openstreetmap.description": {
    en: "aee7952b",
    sv: "Arbeta med platser och koordinater. Med Plats-steget väljer du en punkt på en karta (eller slår upp en stad/adress) och skickar ut dess koordinat; Slå upp en plats gör en koordinat till ett platsnamn. Passar naturligt ihop med OpenWeather — välj eller slå upp ett ställe och koppla sedan koordinaten till en väderuppslagning.",
  },
  "openstreetmap.technical_notes": {
    en: "699ee292",
    sv: "Båda stegen visar en OpenStreetMap-kartväljare på kortet (slå av den med 'Visa karta på kortet' för ett smalare steg). Plats geokodar en skriven eller inkopplad plats; Slå upp en plats bakåtgeokodar den valda eller inkopplade koordinaten. Geokodningen anropar OpenStreetMaps Nominatim-tjänst vid körning genom den SSRF-skyddade HTTP-klienten med en identifierande User-Agent — inget konto och ingen nyckel. Nominatims publika tjänst är hastighetsbegränsad (~1 förfrågan/sekund); för tyngre användning kör du en egen och sätter DAZYFLOW_NOMINATIM_URL. © OpenStreetMap contributors.",
  },
  "openweather.description": {
    en: "61bbf141",
    sv: "Läs vädret för vilken punkt som helst på kartan. Ge ett steg en koordinat — skriven, eller inkopplad från en geokodning, ett formulärfält eller en enhets GPS — och få aktuellt väderläge (en sammanfattning på en rad, temperaturen och ett Clear/Rain/Snow-ord du kan förgrena på) eller en 5-dygnsprognos. Bygg ett flöde som \"sms:a mig om det regnar i morgon\", en morgonbriefing eller en frostvarning för växthuset.",
  },
  "openweather.technical_notes": {
    en: "ca2d1566",
    sv: "Bygger på OpenWeathers kostnadsfria ändpunkter — Current Weather (GET data/2.5/weather) och 5-dygnsprognosen i 3-timmarssteg (GET data/2.5/forecast), som prognosdroppen sammanställer till min/max + väderläge per dag. Fungerar med vilken vanlig API-nyckel som helst på den kostnadsfria planen — ingen betald 'One Call by Call'-prenumeration behövs. Autentiseras med din API-nyckel (appid), som lagras en gång på integrationssidan som en anslutning per organisation och matas in vid körning — ingen nyckel på steget. Enheter kan vara metric (°C, m/s), imperial (°F, mph) eller standard (K, m/s).",
  },
  "postgres.description": {
    en: "9bfec398",
    sv: "Infoga, uppdatera och läsa rader i en Postgres-databas. Kombinera det med Sheets-, Excel- eller webhook-stegen för att hålla databasen i synk med den källa ditt team utgår från.",
  },
  "postgres.technical_notes": {
    en: "211c32d2",
    sv: "Anslutningsregister med pgxpool per (organisation, DSN) och lat utrensning av inaktiva anslutningar. Skicka DSN:en via ${secret.postgres_dsn} från det krypterade hemlighetslagret i stället för att bädda in den i flödets JSON; steget secret_set kan rotera den utan att flödena ändras.",
  },
  "slack.description": {
    en: "5ab3ab5c",
    sv: "Skicka meddelanden från dina flöden, och starta flöden när någon @-nämner din bot. Anslut en arbetsyta en gång och din bot kan lägga upp meddelanden i alla kanaler den är medlem i — praktiskt för aviseringar, dagliga rapporter eller enkla bottar som gör chattmeddelanden till handling.",
  },
  "slack.technical_notes": {
    en: "5761fe68",
    sv: "OAuth 2.0 med scopen chat:write, channels:read och channels:history. Triggern slack_on_mention använder Slacks Events API — peka din Slack-apps Event Subscription-URL på /api/v1/events/slack/<tenant> och sätt DAZYFLOW_SLACK_SIGNING_SECRET på daemonen (HMAC-SHA256-signaturverifiering + 5 minuters replayfönster).",
  },
  "smhi.description": {
    en: "39c4455a",
    sv: "Kostnadsfritt nordiskt väder från Sveriges meteorologiska institut — inget konto och ingen API-nyckel. Ge SMHI Väder-stegen en koordinat (välj den med ett Plats-steg) och få aktuellt väderläge eller en flerdygnsprognos för vilken punkt som helst i Norden och området omkring, i metriska enheter.",
  },
  "smhi.technical_notes": {
    en: "60c57f10",
    sv: "Bygger på SMHI:s Open Data-prognos-API (punktändpunkten i snow1g v1: GET …/geotype/point/lon/{lon}/lat/{lat}/data.json?parameters=…) — utan nyckel. Alltid metriskt (°C, m/s); värdena i symbol_code (Wsymb2 1–27) mappas till beskrivningar, och prognosdroppen sammanställer stegen under dygnet till min/max + väderläge per dag (UTC-dygn). Täckningen är SMHI:s modellområde — en punkt utanför det ger 'out of bounds'.",
  },
  "sqlite.description": {
    en: "79207676",
    sv: "Infoga, uppdatera och läsa rader i en SQLite-fil i din arbetsyta. Passar bra för tillfälliga databaser per organisation, för att prototypa flöden innan du sätter upp en riktig databas, eller för att hålla en liten referenstabell intill dina andra filer i arbetsytan.",
  },
  "sqlite.technical_notes": {
    en: "f9b435a6",
    sv: "Ingen anslutningspool — att öppna filen tar mikrosekunder, så en ny handtag per anrop går bra. .sqlite-filen ligger i arbetsytans sandlåda som alla andra filer där; sandlådans regler gäller.",
  },
  "mcp.description": {
    en: "1a4554a8",
    sv: "Steg som kommer från en MCP-server som din organisation har lagt till, i stället för från en koppling som vi har skrivit. Peka Dazyflow mot en servers adress under Admin → MCP-servrar, så dyker varje verktyg den publicerar upp här som ett steg — inget att installera, och ingen koppling att vänta på. Servern har sin egen inloggning, så dessa steg behöver ingen separat anslutning.",
  },
  "mcp.technical_notes": {
    en: "ec20240d",
    sv: "Verktygen läses vid handskakningen över streamable HTTP (MCP-revision 2025-11-25) och blir steg med id:t mcp:<server>:<verktyg>; ett verktygs argument blir stegets pinnar. Varje server tillhör den organisation som registrerade den och kan bara nås av den organisationens flöden. En server som slutar svara behåller sina steg beskrivna — de visar en \"Kräver anslutning\"-banner och vägrar att köra — så flöden som använder dem behåller sina kopplingar.",
  },
  "standard-library.description": {
    en: "23ca5ac2",
    sv: "Inbyggda flödesprimitiver som inte hör till någon leverantör: dirigering (branch, split_rows, route_rows), väntan (await_approval, sleep), filhantering (läs, skriv), omvandlingsfamiljen (map / sort / dedupe / join / group / compute), databasdroppar (Postgres / MySQL / SQLite) och schematriggrar (cron, poll, webhook). Verktygslådan du tar till mellan tredjepartsanslutningarna.",
  },
  "stripe.description": {
    en: "44844cdb",
    sv: "Reagera på betalningar i samma stund de sker — lyckade, misslyckade eller en avslutad prenumeration — och gör något med dem: skapa en kund, mejla en faktura, dela ut en betallänk eller gör en återbetalning. Bygg ett påminnelseflöde som jagar en misslyckad betalning, en välkomstserie vid en kunds första betalning, eller en direktavisering när någon säger upp sig.",
  },
  "stripe.technical_notes": {
    en: "925a9244",
    sv: "Åtgärderna autentiseras med din hemliga Stripe-nyckel, som anges en gång som Stripe-anslutningen på den här sidan (lagras krypterad som conn.stripe.api_key) och matas in vid körning — ingen nyckel på steget eller i flödet. Triggrarna för betalning, misslyckad betalning och avslutad prenumeration är Stripe-webhooks: peka en ändpunkt på /api/v1/events/stripe/<tenant>, prenumerera på motsvarande händelser (payment_intent.succeeded, payment_intent.payment_failed, customer.subscription.deleted) och spara ändpunktens signeringshemlighet (whsec_…) som STRIPE_WEBHOOK_SECRET — varje leverans Stripe-Signature verifieras mot den. Föredrar du pollning framför webhooks? Bygg Schema → Lista händelser i stället.",
  },
  "twilio.description": {
    en: "43d2e415",
    sv: "Skicka SMS till vilken telefon som helst, direkt från ett flöde. Ta det när en avisering behöver landa i någons ficka — ett \"ordern är skickad\" eller en tidspåminnelse till en kund, en verifieringskod, ett jourlarm, eller ett tips i samma stund en trigger utlöses.",
  },
  "twilio.technical_notes": {
    en: "788efd83",
    sv: "Autentiseras med ditt Twilio Account SID och Auth Token, som anges en gång som Twilio-anslutningen på den här sidan (lagras krypterat som conn.twilio.*) och matas in vid körning — inga uppgifter på steget eller i flödet. Skickar via Twilios Messages API; 'Från' måste vara ett av dina Twilio-nummer i E.164 (+15551234567), eller så anger du ett Messaging Service SID (MG…) i stället.",
  },
  "webhook.description": {
    en: "88c2043c",
    sv: "Skicka en avisering till vilken URL som helst — Slacks incoming-webhook-URL:er, Discord, Teams, PagerDuty eller din egen mottagare. Ta det här när tjänsten inte har en egen anslutning här, eller när du vill ha den enklaste möjliga leveransen utan svar.",
  },
};
