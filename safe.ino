// Simple sketch to act as a safe controller
// Safe can be in 3 modes; locked or open one time, or unlocked
// Commands should be : separated and terminated with a :
// Each command begins with a : sign
//   :PING:msg:    -- returns PINGACK msg - used to test comms and synchronise
//   :STATUS::     -- returns the locked/unlocked state
//   :LOCK:pswd:   -- sets the lock password, sets the state to locked
//   :UNLOCK:pswd: -- Validates the password, sets the state to unlock once
//   :TEST:pswd:   -- tests the password, doesn't unlock
//   :CLEAR:pswd:  -- Sets state to unlocked and clears the password
//   :OPEN:x:      -- Opens the safe for x seconds (default to 5 if not sent)
// The password is stored in EEPROM so it is retained over a reboot.
// On restart it is read back in.  We have a magic string so random data
// isn't mis-read as a password.

#include <Arduino.h>
#include <EEPROM.h>
#include <SoftwareSerial.h>

// This is pin D7 on most boards; this is the pin that needs to be
// connected to the relay
#define pin 7

// What pin causes the LED to flash
#define ledPin 13

// These pins are for software serial
#define rxPin 10
#define txPin 11

SoftwareSerial mySerial(rxPin, txPin, true);
// #define mySerial Serial

// Passwords long than this aren't allowed
#define maxpwlen 100

void setup();
void loop();

enum safestate {
  UNLOCKED,
  LOCKED,
  OPENONCE
};

int state=UNLOCKED;
String pswd;

String get_eeprom()
{
  char d[maxpwlen];
  for (int i=0; i<maxpwlen; i++)
  {
    d[i]=EEPROM.read(i);
  }
  return String(d);
}

void set_eeprom(String s)
{
  for(int i=0; i < s.length(); i++)
  {
    EEPROM.write(i,s[i]);
  }
  EEPROM.write(s.length(),0);
}

void setup()
{
  mySerial.begin(9600);
  pinMode(pin,OUTPUT);
  digitalWrite(pin,HIGH);

  pinMode(ledPin, OUTPUT);

  // Try reading a string from the EEPROM
  pswd=get_eeprom();
  if (pswd.startsWith("PSWD:"))
  {
    state=LOCKED;
    pswd=pswd.substring(5);
  }
  else
  {
    set_eeprom("");
    pswd="";
  }
}

void opensafe(String d)
{
  if (state == LOCKED)
  {
    mySerial.println("ERROR State is locked");
  }
  else
  {
    int del=d.toInt();
    if (del==0) { del=5; }
    if (state == OPENONCE ) { state=LOCKED; }
    digitalWrite(pin,LOW);
    while (del > 0)
    {
      mySerial.println("OK opening safe for " + String(del) + " seconds");
      delay(1000);
      del--;
    }
    digitalWrite(pin,HIGH);
    mySerial.println("OK completed");
  }
}

void status(boolean d)
{
  if (d) { mySerial.print("OK "); }
  mySerial.print("Safe is ");
  if (state==OPENONCE) { mySerial.print("One time un"); }
  if (state==UNLOCKED) { mySerial.print("un"); }
  mySerial.println("locked");
}

void lock(String val)
{
  if (val.length()>maxpwlen)
  {
    mySerial.println("ERROR Password too long");
  }
  else if (state==UNLOCKED)
  {
    set_eeprom("PSWD:"+val);
    pswd=val;
    state=LOCKED;
    mySerial.println("OK Safe locked");
  }
  else
  {
    mySerial.println("ERROR Safe already locked");
  }
}

void unlock(String val,boolean clear, boolean testonly)
{
  if (state==UNLOCKED)
  {
    mySerial.println("ERROR Safe already unlocked");
  }
  else if (val != pswd)
  {
    delay(1000);
    mySerial.println("ERROR Wrong password");
  }
  else if (testonly)
  {
    mySerial.println("OK Passwords match");
  }
  else if (clear)
  {
    set_eeprom("");
    pswd="";
    state=UNLOCKED;
    mySerial.println("OK Safe unlocked");
  }
  else
  {
    state=OPENONCE;
    mySerial.println("OK Safe unlocked for one time");
  }
}

void loop()
{
  String inp;
  int ledstate=0;
  
  mySerial.print("OK Safe code started.  ");
  status(false);
  
  while(1)
  {
    String cmd="";
    while (cmd=="")
    {
      cmd=mySerial.readStringUntil(':');
      cmd.toUpperCase();
      digitalWrite(ledPin, ledstate?LOW:HIGH);
      ledstate=!ledstate;
    }
    String val=mySerial.readStringUntil(':');

         if (cmd=="PING") { mySerial.println("PINGACK:"+val+":"); }
    else if (cmd=="STATUS") { status(true); }
    else if (cmd=="OPEN") { opensafe(val); }
    else if (cmd=="LOCK") { lock(val); }
    else if (cmd=="UNLOCK") { unlock(val,false,false); }
    else if (cmd=="CLEAR") { unlock(val,true,false); }
    else if (cmd=="TEST")  { unlock(val,false,true); }
    else { mySerial.println("ERROR Unknown command: "+cmd); }
  }
}
