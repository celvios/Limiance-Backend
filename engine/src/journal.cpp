#include "limiance/engine/journal.hpp"

#include <array>
#include <cerrno>
#include <cstring>
#include <fstream>
#include <limits>
#include <string>

#include <fcntl.h>
#ifdef _WIN32
#include <io.h>
#include <sys/stat.h>
#else
#include <unistd.h>
#endif

namespace limiance::engine {
namespace {
constexpr std::uint32_t magic=0x314A4D4CUL;
constexpr std::uint16_t version=1;
constexpr std::size_t header_size=24;

void put16(std::uint8_t* out,std::uint16_t value){out[0]=static_cast<std::uint8_t>(value);out[1]=static_cast<std::uint8_t>(value>>8);}
void put32(std::uint8_t* out,std::uint32_t value){for(int i=0;i<4;++i)out[i]=static_cast<std::uint8_t>(value>>(i*8));}
void put64(std::uint8_t* out,std::uint64_t value){for(int i=0;i<8;++i)out[i]=static_cast<std::uint8_t>(value>>(i*8));}
std::uint16_t get16(const std::uint8_t* in){return static_cast<std::uint16_t>(in[0])|static_cast<std::uint16_t>(in[1])<<8;}
std::uint32_t get32(const std::uint8_t* in){std::uint32_t value{};for(int i=0;i<4;++i)value|=static_cast<std::uint32_t>(in[i])<<(i*8);return value;}
std::uint64_t get64(const std::uint8_t* in){std::uint64_t value{};for(int i=0;i<8;++i)value|=static_cast<std::uint64_t>(in[i])<<(i*8);return value;}

std::uint32_t checksum(std::span<const std::uint8_t> data){
  std::uint32_t crc=0xFFFFFFFFU;
  for(const auto byte:data){crc^=byte;for(int bit=0;bit<8;++bit)crc=(crc>>1)^(0x82F63B78U&static_cast<std::uint32_t>(-static_cast<std::int32_t>(crc&1U)));}
  return ~crc;
}

void write_all(int descriptor,std::span<const std::uint8_t> bytes){
  while(!bytes.empty()) {
#ifdef _WIN32
    const auto chunk=static_cast<unsigned int>(std::min<std::size_t>(bytes.size(),std::numeric_limits<unsigned int>::max()));
    const auto written=::_write(descriptor,bytes.data(),chunk);
#else
    const auto written=::write(descriptor,bytes.data(),bytes.size());
#endif
    if(written<0) { if(errno==EINTR) continue; throw JournalError("journal write failed: "+std::string(std::strerror(errno))); }
    if(written==0) throw JournalError("journal write returned zero");
    bytes=bytes.subspan(static_cast<std::size_t>(written));
  }
}

int sync_file(int descriptor) {
#ifdef _WIN32
  return ::_commit(descriptor);
#else
  return ::fdatasync(descriptor);
#endif
}
}

Journal::Journal(std::filesystem::path path):path_(std::move(path)){
  if(path_.empty())throw JournalError("journal path is required");
  if(path_.has_parent_path())std::filesystem::create_directories(path_.parent_path());
#ifdef _WIN32
  descriptor_=::_open(path_.string().c_str(),O_CREAT|O_APPEND|O_WRONLY|O_BINARY,_S_IREAD|_S_IWRITE);
#else
  descriptor_=::open(path_.c_str(),O_CREAT|O_APPEND|O_WRONLY,0640);
#endif
  if(descriptor_<0) throw JournalError("open journal failed: "+std::string(std::strerror(errno)));
}
Journal::~Journal(){if(descriptor_>=0){
#ifdef _WIN32
  ::_close(descriptor_);
#else
  ::close(descriptor_);
#endif
}}

void Journal::append(JournalKind kind,std::uint64_t sequence,std::span<const std::uint8_t> payload){
  if(sequence==0||payload.empty()||payload.size()>std::numeric_limits<std::uint32_t>::max())throw JournalError("invalid journal record");
  std::array<std::uint8_t,header_size> header{};put32(header.data(),magic);put16(header.data()+4,version);put16(header.data()+6,static_cast<std::uint16_t>(kind));put64(header.data()+8,sequence);put32(header.data()+16,static_cast<std::uint32_t>(payload.size()));put32(header.data()+20,checksum(payload));
  write_all(descriptor_,header);
  write_all(descriptor_,payload);
  if(sync_file(descriptor_)!=0) throw JournalError("journal sync failed: "+std::string(std::strerror(errno)));
}

std::vector<JournalRecord> Journal::recover()const{
  std::ifstream input(path_,std::ios::binary);if(!input)throw JournalError("open journal for recovery failed");std::vector<JournalRecord> records;std::uint64_t previous{};
  while(true) {
    std::array<std::uint8_t,header_size> header{};
    input.read(reinterpret_cast<char*>(header.data()),static_cast<std::streamsize>(header.size()));
    if(input.gcount()==0&&input.eof()) break;
    if(input.gcount()!=static_cast<std::streamsize>(header.size())) throw JournalError("truncated journal header");
    if(get32(header.data())!=magic||get16(header.data()+4)!=version) throw JournalError("invalid journal header");
    const auto sequence=get64(header.data()+8);
    if(sequence!=previous+1) throw JournalError("non-contiguous journal sequence");
    const auto length=get32(header.data()+16);
    if(length==0||length>(8U<<20)) throw JournalError("invalid journal payload length");
    std::vector<std::uint8_t> payload(length);
    input.read(reinterpret_cast<char*>(payload.data()),static_cast<std::streamsize>(payload.size()));
    if(input.gcount()!=static_cast<std::streamsize>(payload.size())) throw JournalError("truncated journal payload");
    if(checksum(payload)!=get32(header.data()+20)) throw JournalError("journal checksum mismatch");
    records.push_back(JournalRecord{static_cast<JournalKind>(get16(header.data()+6)),sequence,std::move(payload)});
    previous=sequence;
  }
  return records;
}

}  // namespace limiance::engine
